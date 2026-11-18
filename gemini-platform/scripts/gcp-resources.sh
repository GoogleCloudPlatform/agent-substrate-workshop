#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Enables required Google Cloud APIs, creates the snapshot bucket, Artifact Registry repo, and
# least-privilege IAM bindings, and asynchronously starts the regional ingress Gateway and
# managed session/memory runtime engine so downstream modules have zero provisioning wait.
source "$(dirname "$0")/lib.sh"
load_env

PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"
state_put project-number "$PROJECT_NUMBER"

ORG_ID="$(gcloud projects get-ancestors "$PROJECT_ID" --format=json 2>/dev/null | jq -r '.[] | select(.type=="organization") | .id // empty' | head -n1 || true)"
if [[ "${ENABLE_POD_AGENT_IDENTITY:-false}" == "true" && -n "$ORG_ID" ]]; then
  state_put org-id "$ORG_ID"
else
  rm -f "$STATE_DIR/org-id"
fi
export PROJECT_NUMBER
resolve_spiffe_identity

WI="principal://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject"
COMPUTE_SA="serviceAccount:${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"

step "Enable core and Gemini platform APIs"
gcloud services enable --project "$PROJECT_ID" \
  serviceusage.googleapis.com cloudresourcemanager.googleapis.com compute.googleapis.com \
  container.googleapis.com storage.googleapis.com artifactregistry.googleapis.com \
  iam.googleapis.com iamcredentials.googleapis.com \
  aiplatform.googleapis.com modelarmor.googleapis.com \
  networkservices.googleapis.com networksecurity.googleapis.com apphub.googleapis.com \
  logging.googleapis.com monitoring.googleapis.com cloudtrace.googleapis.com telemetry.googleapis.com
for optional_api in agentregistry.googleapis.com agentidentity.googleapis.com; do
  gcloud services enable "$optional_api" --project "$PROJECT_ID" >/dev/null 2>&1 || \
    warn "could not enable $optional_api (continuing with in-cluster governance fallbacks)"
done
ok "APIs enabled"

step "Bucket gs://${BUCKET_NAME}"
if ! gcloud storage buckets describe "gs://${BUCKET_NAME}" --project "$PROJECT_ID" >/dev/null 2>&1; then
  gcloud storage buckets create "gs://${BUCKET_NAME}" --project "$PROJECT_ID" --location "$REGION" --uniform-bucket-level-access
fi
ok "bucket ready"

step "Artifact Registry ${AR_REPO}"
if ! gcloud artifacts repositories describe "$AR_REPO" --location "$REGION" --project "$PROJECT_ID" >/dev/null 2>&1; then
  gcloud artifacts repositories create "$AR_REPO" --repository-format=docker --location "$REGION" --project "$PROJECT_ID"
fi
gcloud auth configure-docker "${REGION}-docker.pkg.dev" --quiet >/dev/null
ok "registry ready: ${KO_DOCKER_REPO}"

_retry_gcloud() {
  local i
  for ((i = 1; i <= 6; i++)); do
    "$@" >/dev/null 2>&1 && return 0
    (( i < 6 )) && sleep 5
  done
  "$@" >/dev/null
}
bind_bucket()  { _retry_gcloud gcloud storage buckets add-iam-policy-binding "gs://${BUCKET_NAME}" --member="$1" --role="$2" --condition=None; }
bind_project() { _retry_gcloud gcloud projects add-iam-policy-binding "$PROJECT_ID" --member="$1" --role="$2" --condition=None; }
bind_gsa()     { _retry_gcloud gcloud iam service-accounts add-iam-policy-binding "$1" --project "$PROJECT_ID" --member="$2" --role="$3" --condition=None; }
bind_repo()    { _retry_gcloud gcloud artifacts repositories add-iam-policy-binding "$AR_REPO" --location "$REGION" --project "$PROJECT_ID" --member="$1" --role="$2"; }

TRANSFER_GSA_NAME="${SNAPSHOT_TRANSFER_GSA_NAME:-ate-snapshot-transfer}"
TRANSFER_GSA_EMAIL="${TRANSFER_GSA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
CREATED_TRANSFER_GSA=false
step "Snapshot transfer GSA (${TRANSFER_GSA_EMAIL})"
if ! gcloud iam service-accounts describe "$TRANSFER_GSA_EMAIL" --project "$PROJECT_ID" >/dev/null 2>&1; then
  gcloud iam service-accounts create "$TRANSFER_GSA_NAME" --project "$PROJECT_ID" --display-name "Substrate snapshot transfers" >/dev/null
  CREATED_TRANSFER_GSA=true
fi
ok "snapshot transfer GSA ready"

step "IAM for Agent Substrate (ate-system + snapshot transfer GSA)"
for sa in atelet ate-api-server; do
  bind_bucket "${WI}/ns/ate-system/sa/${sa}" roles/storage.objectAdmin
  bind_bucket "${WI}/ns/ate-system/sa/${sa}" roles/storage.bucketViewer
  bind_repo   "${WI}/ns/ate-system/sa/${sa}" roles/artifactregistry.reader
done
bind_bucket "serviceAccount:${TRANSFER_GSA_EMAIL}" roles/storage.objectAdmin
bind_bucket "serviceAccount:${TRANSFER_GSA_EMAIL}" roles/storage.bucketViewer
bind_gsa    "$TRANSFER_GSA_EMAIL" "${WI}/ns/ate-system/sa/ate-api-server" roles/iam.serviceAccountTokenCreator
bind_gsa    "$TRANSFER_GSA_EMAIL" "${WI}/ns/ate-system/sa/ate-snapshot-controller" roles/iam.workloadIdentityUser
bind_bucket "$COMPUTE_SA" roles/storage.objectViewer
bind_repo   "$COMPUTE_SA" roles/artifactregistry.reader
if $CREATED_TRANSFER_GSA && kubectl -n ate-system get deploy ate-api-server >/dev/null 2>&1; then
  step "Restarting ate-api-server to pick up newly created ${TRANSFER_GSA_EMAIL}"
  kubectl -n ate-system rollout restart deploy/ate-api-server >/dev/null
  kubectl -n ate-system rollout status deploy/ate-api-server --timeout=180s
fi
ok "Substrate IAM bound"

step "IAM for Governed Broker (${NAMESPACE}/sre-gateway -> Gemini platform)"
WIF_GATEWAY_MEMBER="${WI}/ns/${NAMESPACE}/sa/sre-gateway"
AGENT_GATEWAY_MEMBER="${WORKLOAD_PRINCIPAL_PREFIX}/ns/${NAMESPACE}/sa/sre-gateway"
GATEWAY_MEMBERS=("$WIF_GATEWAY_MEMBER")
if [[ "$AGENT_GATEWAY_MEMBER" != "$WIF_GATEWAY_MEMBER" ]]; then
  GATEWAY_MEMBERS+=("$AGENT_GATEWAY_MEMBER")
elif [[ -n "${ORG_ID:-}" ]]; then
  GATEWAY_MEMBERS+=("principal://agents.global.org-${ORG_ID}.system.id.goog/resources/container/projects/${PROJECT_NUMBER}/locations/${CLUSTER_LOCATION:-${REGION}}/clusters/${CLUSTER_NAME}/ns/${NAMESPACE}/sa/sre-gateway")
fi
for gw_member in "${GATEWAY_MEMBERS[@]}"; do
  for role in roles/aiplatform.user roles/modelarmor.user roles/logging.viewer roles/container.viewer roles/cloudtrace.agent roles/monitoring.viewer roles/serviceusage.serviceUsageConsumer; do
    bind_project "$gw_member" "$role"
  done
  for opt_role in roles/telemetry.tracesWriter roles/agentregistry.viewer roles/agentidentity.user; do
    gcloud projects add-iam-policy-binding "$PROJECT_ID" --member="$gw_member" --role="$opt_role" --condition=None >/dev/null 2>&1 || true
  done
done
ok "Governed Broker IAM bound"

step "Apply quota-safe ComputeClass and async regional ingress Gateway"
if kubectl get nodes >/dev/null 2>&1; then
  if kubectl get crd computeclasses.cloud.google.com >/dev/null 2>&1; then
    "$REPO_ROOT/scripts/render.sh" "$REPO_ROOT/modules/00-setup/manifests/computeclass.yaml.tmpl" | kubectl apply -f -
    ok "ComputeClass/substrate-gvisor applied (nodePoolAutoCreation=${ENABLE_COMPUTE_CLASS_AUTOPROVISION})"
  else
    warn "computeclasses.cloud.google.com CRD not yet served; run make computeclass once the autoscaler initializes"
  fi

  rendered_gw="$("$REPO_ROOT/scripts/render.sh" "$REPO_ROOT/modules/00-setup/manifests/ingress-gateway.yaml.tmpl")"
  if kubectl get crd gateways.gateway.networking.k8s.io >/dev/null 2>&1 && kubectl get crd gcpagentgateways.networking.gke.io >/dev/null 2>&1; then
    kubectl apply -f - <<<"$rendered_gw"
    ok "regional Gateway + GCPAgentGateway reconciling asynchronously in ${NAMESPACE}"
  elif kubectl get crd gateways.gateway.networking.k8s.io >/dev/null 2>&1; then
    awk '/^kind: GCPAgentGateway/{exit} {print}' <<<"$rendered_gw" | sed '$d' | kubectl apply -f - || true
    ok "regional Gateway reconciling asynchronously in ${NAMESPACE} (GCPAgentGateway CRD not yet installed)"
  else
    kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
    kubectl label namespace "$NAMESPACE" ate.dev/api-access=true ate.dev/gateway-access=true --overwrite >/dev/null
    warn "Gateway API CRDs not installed on cluster; created namespace ${NAMESPACE} only"
  fi
fi

step "Start async Gemini Sessions / Memory Bank runtime discovery"
nohup bash -c '
  set -euo pipefail
  export CLOUDSDK_CORE_DISABLE_USAGE_REPORTING=true
  export CLOUDSDK_CORE_DISABLE_PROMPTS=1
  source "$1/scripts/lib.sh"
  load_env
  cba_cert="$(gcloud config get-value context_aware/use_client_certificate 2>/dev/null || true)"
  if [[ "$cba_cert" == "true" ]]; then
    state_put reasoning-engine-id auto
    echo "Context-Aware Access client certificate enabled on workstation; sre-gateway will discover or provision ReasoningEngine in-cluster via Workload Identity"
    exit 0
  fi
  api="https://${REGION}-aiplatform.googleapis.com/v1beta1/projects/${PROJECT_ID}/locations/${REGION}"
  tok="$(gcloud auth print-access-token)"
  list_resp="$(curl -fsS -H "Authorization: Bearer $tok" "${api}/reasoningEngines" 2>/dev/null || true)"
  if [[ -z "$list_resp" ]]; then
    state_put reasoning-engine-id auto
    echo "workstation REST probe unavailable; sre-gateway will discover or provision ReasoningEngine in-cluster via Workload Identity"
    exit 0
  fi
  existing="$(jq -r ".reasoningEngines[]? | select(.displayName == \"substrate-301-sre-memory\") | .name" <<<"$list_resp" | head -n1 || true)"
  if [[ -n "$existing" ]]; then
    state_put reasoning-engine-id "${existing##*/}"
    echo "discovered existing runtime: $existing"
    exit 0
  fi
  op="$(curl -fsS -X POST -H "Authorization: Bearer $tok" -H "Content-Type: application/json" \
    -d "{\"displayName\":\"substrate-301-sre-memory\",\"description\":\"Managed session ledger and consolidated memory runtime for Agent Substrate on Gemini platform\"}" \
    "${api}/reasoningEngines" 2>/dev/null | jq -r ".name // empty" || true)"
  if [[ -z "$op" ]]; then
    state_put reasoning-engine-id auto
    echo "runtime creation deferred to sre-gateway Workload Identity (durable broker state volume active)"
    exit 0
  fi
  for _ in $(seq 1 60); do
    sleep 5
    tok="$(gcloud auth print-access-token)"
    res="$(curl -fsS -H "Authorization: Bearer $tok" "https://${REGION}-aiplatform.googleapis.com/v1beta1/${op}" 2>/dev/null || true)"
    if [[ "$(jq -r ".done // false" <<<"$res")" == "true" ]]; then
      re_name="$(jq -r ".response.name // empty" <<<"$res")"
      if [[ -n "$re_name" ]]; then
        state_put reasoning-engine-id "${re_name##*/}"
        echo "provisioned runtime: $re_name"
      fi
      exit 0
    fi
  done
' _ "$REPO_ROOT" >"$STATE_DIR/reasoning-engine.log" 2>&1 </dev/null &
echo $! >"$STATE_DIR/reasoning-engine.pid"
ok "async runtime bootstrap running in background (pid $!, log: .workshop/reasoning-engine.log)"
state_put gcp-complete "$(date -u +%Y-%m-%dT%H:%M:%SZ) PROJECT=$PROJECT_ID NAMESPACE=$NAMESPACE SPIFFE=$WORKLOAD_PRINCIPAL_PREFIX"
