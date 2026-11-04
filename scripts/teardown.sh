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

# teardown.sh [--all] — delete everything the workshop created. Never deletes the GKE cluster (make delete-cluster does),
# and never touches the ate-system Deployments/DaemonSets Substrate itself runs as.
# --all also deletes the snapshot bucket and the Artifact Registry repo (and with them the bucket/repo IAM
# grants), plus the workshop namespace's project IAM bindings and the snapshot transfer service account `make gcp`
# created. In source install mode that leaves the already-running Substrate without the images or IAM grants it
# needs, so it needs a full reinstall before the project is used again.
# Set YES=1 to skip the confirmation prompt.
source "$(dirname "$0")/lib.sh"
load_env
case "${1:-}" in
  "")     ALL=false ;;
  --all)  ALL=true ;;
  *)      die "usage: teardown.sh [--all]" ;;
esac
if [[ "${YES:-}" != 1 ]]; then
  echo "This deletes atespace $ATESPACE (all actors and templates), namespace $NAMESPACE, the workshop and substrate dashboards,"
  echo "and gs://$BUCKET_NAME/$ATESPACE/.$($ALL && echo " ALSO the bucket gs://$BUCKET_NAME, repo $AR_REPO, the snapshot transfer service account, and the workshop's IAM bindings.")"
  read -r -p "Type 'yes' to continue: " answer || answer=""
  [[ "$answer" == yes ]] || die "aborted"
fi
stop_port_forwards

if kubectl ate get atespace "$ATESPACE" >/dev/null 2>&1; then
  step "actors and templates in atespace $ATESPACE"
  for a in $(actor_names "$ATESPACE"); do kubectl ate delete actor "$a" -a "$ATESPACE" || warn "actor $a"; done
  # Actor deletion is asynchronous (an actor moves to DELETING before it's actually gone), so
  # wait for actors to finish deleting before removing their templates and the atespace.
  no_actors_left() { [[ -z "$(actor_names "$ATESPACE")" ]]; }
  ( wait_for 60 "actors in $ATESPACE to finish deleting" no_actors_left ) || warn "some actors in $ATESPACE may still be deleting"
  # List templates instead of hardcoding agent-v1/v2/v3, so a template made outside the guided
  # modules (or a future module) still gets cleaned up. `actor-templates` is a real alias for
  # `actor-template` (kubectl-ate); with no name given it lists every template in the atespace.
  # JSON envelope key is "actorTemplates" (protojson on ListActorTemplatesResponse), not "items".
  for t in $(kubectl ate get actor-templates -a "$ATESPACE" -o json | jq -r '(.actorTemplates // [])[].metadata.name'); do
    kubectl ate delete actor-template "$t" -a "$ATESPACE" 2>/dev/null || true
  done
  kubectl ate delete atespace "$ATESPACE" || warn "atespace $ATESPACE (delete remaining records and retry)"
else
  warn "atespace $ATESPACE not found — skipping actor/template cleanup"
fi

step "namespace $NAMESPACE (WorkerPool, gateway, Prometheus UI)"
# Non-fatal: --ignore-not-found only covers a missing *object*, not a missing WorkerPool CRD
# (e.g. Substrate itself was already removed), which would otherwise abort the rest of teardown
# under set -e.
kubectl -n "$NAMESPACE" delete workerpool agents --ignore-not-found --wait=true || warn "workerpool agents (the WorkerPool CRD may be gone)"
kubectl delete ns "$NAMESPACE" --ignore-not-found || warn "namespace $NAMESPACE"

step "dashboards (the workshop's and substrate's three, as created by make dashboards)"
# Titles come from the same files scripts/dashboards.sh uploads.
dashboard_titles() {
  local f found=false
  echo "Agent Substrate Workshop"
  for f in "$REPO_ROOT"/observability/substrate-dashboards/*.json; do
    [[ -f "$f" ]] || continue
    jq -r .displayName "$f" 2>/dev/null && found=true
  done
  $found || printf '%s\n' "Substrate Routing & E2E Latency" "Substrate gRPC Server — latency / QPS / errors" "Substrate Snapshot Size & QPS"
}
while IFS= read -r title; do
  [[ -n "$title" ]] || continue
  names="$(gcloud monitoring dashboards list --project "$PROJECT_ID" \
    --filter="displayName=\"$title\"" --format='value(name)' 2>/dev/null)" || { warn "could not list dashboard: $title"; continue; }
  for d in $names; do
    gcloud monitoring dashboards delete "$d" --project "$PROJECT_ID" --quiet >/dev/null || warn "dashboard $title ($d)"
  done
done < <(dashboard_titles)
ok "dashboards removed"

step "snapshots"
gcloud storage rm -r "gs://$BUCKET_NAME/$ATESPACE/**" --quiet 2>/dev/null || true

if $ALL; then
  step "bucket, Artifact Registry repo, and the workshop's IAM bindings (undoing 'make gcp')"
  gcloud storage rm -r "gs://$BUCKET_NAME" --quiet || true
  gcloud storage rm -r "gs://${BUCKET_NAME}-assets" --quiet 2>/dev/null || true
  gcloud artifacts repositories delete "$AR_REPO" --location "$REGION" --project "$PROJECT_ID" --quiet || true
  # Deleting the bucket and the repo also drops their IAM policies, including the Artifact Registry
  # reader grants (repo-level, never project-level, so nothing outside the workshop loses access).
  # The project-level bindings `scripts/gcp-resources.sh` created (the workshop namespace's Workload
  # Identity -> Gemini/Monitoring) outlive both, so they need to be removed explicitly.
  PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')" || true
  if [[ -n "${PROJECT_NUMBER:-}" ]]; then
    WI="principal://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject"
    unbind_project() { gcloud projects remove-iam-policy-binding "$PROJECT_ID" --member="$1" --role="$2" --condition=None --quiet >/dev/null 2>&1 || true; }
    unbind_project "${WI}/ns/${NAMESPACE}/sa/gateway" roles/aiplatform.user
    unbind_project "${WI}/ns/${NAMESPACE}/sa/prometheus-ui" roles/monitoring.viewer
    ok "project IAM bindings removed"
  else
    warn "could not resolve the project number; leaving project IAM bindings in place"
  fi
  # `make gcp` also created Substrate's snapshot transfer service account; without the bucket it has nothing to do.
  transfer_gsa="${SNAPSHOT_TRANSFER_GSA_NAME:-ate-snapshot-transfer}@${PROJECT_ID}.iam.gserviceaccount.com"
  if gcloud iam service-accounts delete "$transfer_gsa" --project "$PROJECT_ID" --quiet >/dev/null 2>&1; then
    ok "service account $transfer_gsa deleted"
  else
    warn "could not delete service account $transfer_gsa (it may already be gone)"
  fi
  # Substrate itself is untouched (ate-system isn't ours to delete), but in source mode its
  # control-plane and worker images lived in the Artifact Registry repo just deleted, and the
  # IAM grants that let atelet/ateapi write snapshots are gone too. Force a full reinstall
  # rather than let the next `make install`/catch-up think it's already done.
  rm -f "$STATE_DIR/worker-image" "$STATE_DIR/install-complete"
  warn "Substrate needs a reinstall: its images (source mode) and its GCP IAM grants were just removed. Run make install (or make catch-up) before using this project again."
fi
rm -f "$STATE_DIR"/{agent-image,gateway-image,ate-token,ate-ca.pem,agent-v*.yaml}
if $ALL; then
  ok "teardown complete (the cluster is untouched; Substrate itself is still deployed but needs a reinstall — see above)"
else
  ok "teardown complete (Substrate and the cluster are untouched)"
fi
