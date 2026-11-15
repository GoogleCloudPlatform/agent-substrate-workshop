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

# teardown.sh [--all]: delete workshop 301 resources while preserving the GKE cluster and ate-system.
# Pass --all (make teardown ALL=1) to also remove the bucket, Artifact Registry repo, managed runtime,
# project IAM bindings, and snapshot transfer service account. Set YES=1 to skip confirmation.
source "$(dirname "$0")/lib.sh"
load_env

case "${1:-}" in
  "")    ALL=false ;;
  --all) ALL=true ;;
  *)     die "usage: teardown.sh [--all]" ;;
esac

if [[ "${YES:-}" != 1 ]]; then
  echo "This deletes atespace $ATESPACE, namespaces $NAMESPACE and $SIM_NAMESPACE, ComputeClass/substrate-gvisor,"
  echo "and gs://$BUCKET_NAME/atespaces/$ATESPACE/.$($ALL && echo " ALSO bucket gs://$BUCKET_NAME, repo $AR_REPO, runtime engine, and IAM grants.")"
  read -r -p "Type 'yes' to continue: " answer || answer=""
  [[ "$answer" == yes ]] || die "aborted"
fi

stop_port_forwards

if command -v kubectl-ate >/dev/null 2>&1 && kubectl ate get atespace "$ATESPACE" >/dev/null 2>&1; then
  step "Actors and templates in atespace $ATESPACE"
  for a in $(actor_names "$ATESPACE"); do
    kubectl ate delete actor "$a" -a "$ATESPACE" || warn "actor $a"
  done
  no_actors_left() { [[ -z "$(actor_names "$ATESPACE")" ]]; }
  ( wait_for 60 "actors in $ATESPACE to finish deleting" no_actors_left ) || warn "some actors in $ATESPACE may still be deleting"
  for t in $(kubectl ate get actor-templates -a "$ATESPACE" -o json | jq -r '(.actorTemplates // [])[].metadata.name'); do
    kubectl ate delete actor-template "$t" -a "$ATESPACE" 2>/dev/null || true
  done
  kubectl ate delete atespace "$ATESPACE" || warn "atespace $ATESPACE"
fi

cba_cert="$(gcloud config get-value context_aware/use_client_certificate 2>/dev/null || true)"
teardown_gw_url=""
started_teardown_pf=false
if _is_sre_gateway_url "http://127.0.0.1:8081"; then
  teardown_gw_url="http://127.0.0.1:8081"
elif command -v kubectl >/dev/null 2>&1 &&
     [[ -n "$(kubectl -n "$NAMESPACE" get endpoints sre-gateway -o jsonpath='{.subsets[0].addresses[0].ip}' 2>/dev/null || true)" ]]; then
  if ephemeral_port_forward teardown-gw "$NAMESPACE" svc/sre-gateway 18085 8080 >/dev/null 2>&1; then
    teardown_gw_url="$PF_URL"
    started_teardown_pf=true
  fi
fi
if [[ -n "$teardown_gw_url" ]]; then
  for sid in inc-402 inc-403 inc-409; do
    curl -fsS -X POST "$teardown_gw_url/agentengine/sessions:delete" \
      -H "Content-Type: application/json" \
      -d "{\"app_name\":\"substrate-sre-triage\",\"user_id\":\"sre-operator\",\"session_id\":\"${sid}\"}" >/dev/null 2>&1 || true
  done
  if $started_teardown_pf; then
    stop_port_forward teardown-gw
  fi
fi

step "Namespaces $NAMESPACE and $SIM_NAMESPACE, PodMonitoring, policies, plus ComputeClass/substrate-gvisor"
kubectl -n "$NAMESPACE" delete podmonitoring sre-gateway --ignore-not-found 2>/dev/null || true
kubectl -n "$NAMESPACE" delete gcpegresspolicy default --ignore-not-found 2>/dev/null || true
kubectl -n "$NAMESPACE" delete gcpbackend sre-swp-egress-backend --ignore-not-found 2>/dev/null || true
kubectl -n "$NAMESPACE" delete gcpauthzpolicy sre-ingress-authz-policy --ignore-not-found 2>/dev/null || true
kubectl -n "$NAMESPACE" delete httproute sre-ingress-route --ignore-not-found 2>/dev/null || true
kubectl -n "$NAMESPACE" delete gcpagentgateway sre-client-to-agent-gateway --ignore-not-found --wait=false 2>/dev/null || true
kubectl -n "$NAMESPACE" delete gateway sre-ingress-gateway sre-agent-gateway --ignore-not-found --wait=false 2>/dev/null || true
kubectl -n "$NAMESPACE" delete workerpool --all --ignore-not-found --wait=true 2>/dev/null || true
kubectl -n "$NAMESPACE" delete pvc sre-gateway-state --ignore-not-found --wait=false 2>/dev/null || true
kubectl delete ns "$NAMESPACE" "$SIM_NAMESPACE" --ignore-not-found || warn "namespace cleanup incomplete"
kubectl delete computeclass substrate-gvisor --ignore-not-found 2>/dev/null || true

step "Snapshots under gs://$BUCKET_NAME/atespaces/$ATESPACE/"
gcloud storage rm -r "gs://$BUCKET_NAME/atespaces/$ATESPACE/**" --quiet 2>/dev/null || true
gcloud storage rm -r "gs://$BUCKET_NAME/$ATESPACE/**" --quiet 2>/dev/null || true

tok=""
if [[ "$cba_cert" != "true" ]]; then
  tok="$(gcloud auth print-access-token 2>/dev/null || true)"
fi
re_id="${REASONING_ENGINE_ID:-$(state_get reasoning-engine-id || true)}"
if [[ ( -z "$re_id" || "$re_id" == "auto" ) && -n "$tok" ]]; then
  re_name="$(curl -fsS -H "Authorization: Bearer $tok" \
    "https://${REGION}-aiplatform.googleapis.com/v1beta1/projects/${PROJECT_ID}/locations/${REGION}/reasoningEngines" 2>/dev/null \
    | jq -r '.reasoningEngines[]? | select(.displayName == "substrate-301-sre-memory") | .name' | head -n1 || true)"
  re_id="${re_name##*/}"
fi

# Delete workshop session histories even when --all is omitted so repeated runs
# on a shared project start with clean ReasoningEngine sessions.
if [[ -n "$re_id" && "$re_id" != "auto" && "$re_id" != "broker-durable" && -n "$tok" ]]; then
  step "Workshop sessions (inc-402, inc-403, inc-409) in ReasoningEngine $re_id"
  for sid in inc-402 inc-403 inc-409; do
    curl -fsS -X DELETE -H "Authorization: Bearer $tok" \
      "https://${REGION}-aiplatform.googleapis.com/v1beta1/projects/${PROJECT_ID}/locations/${REGION}/reasoningEngines/${re_id}/sessions/${sid}" >/dev/null 2>&1 || true
  done
  while IFS= read -r cloud_sess_name; do
    [[ -n "$cloud_sess_name" ]] || continue
    curl -fsS -X DELETE -H "Authorization: Bearer $tok" \
      "https://${REGION}-aiplatform.googleapis.com/v1beta1/${cloud_sess_name}" >/dev/null 2>&1 || true
  done < <(curl -fsS -H "Authorization: Bearer $tok" \
    "https://${REGION}-aiplatform.googleapis.com/v1beta1/projects/${PROJECT_ID}/locations/${REGION}/reasoningEngines/${re_id}/sessions" 2>/dev/null \
    | jq -r '.sessions[]? | select((.name | test("/sessions/(inc-402|inc-403|inc-409)$")) or .sessionState.incident_id == "INC-402" or .sessionState.incident_id == "INC-403" or .sessionState.incident_id == "INC-409") | .name' 2>/dev/null || true)
fi

if $ALL; then
  step "Managed runtime, Skill Registry, Model Armor, bucket, Artifact Registry repo, and workshop IAM bindings"
  if [[ -n "$re_id" && "$re_id" != "auto" && "$re_id" != "broker-durable" && -n "$tok" ]]; then
    curl -fsS -X DELETE -H "Authorization: Bearer $tok" \
      "https://${REGION}-aiplatform.googleapis.com/v1beta1/projects/${PROJECT_ID}/locations/${REGION}/reasoningEngines/${re_id}?force=true" >/dev/null 2>&1 || true
  fi
  skill_loc="${SKILL_REGISTRY_LOCATION:-global}"
  for cloud_skill in private-ledger-pool-triage ledger-pool-triage; do
    CLOUDSDK_CORE_INTERACTIVE_UX_STYLE=OFF gcloud alpha agent-registry skills update "$cloud_skill" \
      --location="$skill_loc" --project="$PROJECT_ID" --target-state=target-state-disabled --http-timeout=15 --quiet >/dev/null 2>&1 || true
    while IFS= read -r rev_name; do
      [[ -n "$rev_name" ]] || continue
      CLOUDSDK_CORE_INTERACTIVE_UX_STYLE=OFF gcloud alpha agent-registry skills revisions delete "${rev_name##*/}" \
        --skill="$cloud_skill" --location="$skill_loc" --project="$PROJECT_ID" --http-timeout=15 --quiet >/dev/null 2>&1 || true
    done < <(CLOUDSDK_CORE_INTERACTIVE_UX_STYLE=OFF gcloud alpha agent-registry skills revisions list \
      --skill="$cloud_skill" --location="$skill_loc" --project="$PROJECT_ID" --format='value(name)' --http-timeout=10 --quiet 2>/dev/null || true)
    CLOUDSDK_CORE_INTERACTIVE_UX_STYLE=OFF gcloud alpha agent-registry skills delete "$cloud_skill" \
      --location="$skill_loc" --project="$PROJECT_ID" --http-timeout=30 --quiet >/dev/null 2>&1 || true
    if [[ -n "$tok" ]]; then
      curl -fsS -X DELETE -H "Authorization: Bearer $tok" \
        "https://agentregistry.googleapis.com/v1/projects/${PROJECT_ID}/locations/${skill_loc}/skills/${cloud_skill}?force=true" >/dev/null 2>&1 || true
    fi
  done
  gcloud storage rm -r "gs://$BUCKET_NAME" --quiet 2>/dev/null || true
  gcloud artifacts repositories delete "$AR_REPO" --location "$REGION" --project "$PROJECT_ID" --quiet 2>/dev/null || true

  PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)' 2>/dev/null || true)"
  if [[ -n "$PROJECT_NUMBER" ]]; then
    export PROJECT_NUMBER
    resolve_spiffe_identity
    WI="principal://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject"
    WIF_GATEWAY_MEMBER="${WI}/ns/${NAMESPACE}/sa/sre-gateway"
    AGENT_GATEWAY_MEMBER="${WORKLOAD_PRINCIPAL_PREFIX}/ns/${NAMESPACE}/sa/sre-gateway"
    GATEWAY_MEMBERS=("$WIF_GATEWAY_MEMBER")
    if [[ "$AGENT_GATEWAY_MEMBER" != "$WIF_GATEWAY_MEMBER" ]]; then
      GATEWAY_MEMBERS+=("$AGENT_GATEWAY_MEMBER")
    fi
    for gw_member in "${GATEWAY_MEMBERS[@]}"; do
      for role in roles/aiplatform.user roles/modelarmor.user roles/logging.viewer roles/container.viewer roles/cloudtrace.agent roles/monitoring.viewer roles/serviceusage.serviceUsageConsumer roles/telemetry.tracesWriter roles/agentregistry.viewer roles/agentidentity.user; do
        gcloud projects remove-iam-policy-binding "$PROJECT_ID" --member="$gw_member" --role="$role" --condition=None --quiet >/dev/null 2>&1 || true
      done
    done
  fi

  transfer_gsa="${SNAPSHOT_TRANSFER_GSA_NAME:-ate-snapshot-transfer}@${PROJECT_ID}.iam.gserviceaccount.com"
  gcloud iam service-accounts delete "$transfer_gsa" --project "$PROJECT_ID" --quiet >/dev/null 2>&1 || true
  ma_tmpl="$(state_get model-armor-template || true)"
  if [[ -n "$ma_tmpl" && "$ma_tmpl" != "none" ]]; then
    gcloud model-armor templates delete "$ma_tmpl" --location="$REGION" --project="$PROJECT_ID" --quiet >/dev/null 2>&1 || true
  fi
  ap_res="$(state_get auth-provider-id || true)"
  if [[ -n "$ap_res" && "$ap_res" != "none" ]]; then
    gcloud agent-identity auth-providers delete "${ap_res##*/}" --location="$REGION" --project="$PROJECT_ID" --quiet >/dev/null 2>&1 || true
  fi
  rm -f "$STATE_DIR/worker-image" "$STATE_DIR/install-complete" "$STATE_DIR/reasoning-engine-id" "$STATE_DIR/model-armor-template" "$STATE_DIR/auth-provider-id"
fi

rm -f "$STATE_DIR"/{gcp-complete,agent-image,gateway-image,ledger-api-image,ops-hub-image,sre-agent-v*.yaml,governance-enforced,require-3lo,oauth-client-secret,gateway-ip,observability-enabled,podmonitoring.yaml,eval-report.json}
ok "teardown complete"
