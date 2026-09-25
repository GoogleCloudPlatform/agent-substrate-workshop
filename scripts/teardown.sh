#!/usr/bin/env bash
# teardown.sh [--all] — delete everything the workshop created. Keeps the cluster and Substrate itself.
# --all also deletes the snapshot bucket, the Artifact Registry repo, and the project IAM bindings
# `make gcp` created. Set YES=1 to skip the confirmation prompt.
source "$(dirname "$0")/lib.sh"
load_env
case "${1:-}" in
  "")     ALL=false ;;
  --all)  ALL=true ;;
  *)      die "usage: teardown.sh [--all]" ;;
esac
if [[ "${YES:-}" != 1 ]]; then
  echo "This deletes atespace $ATESPACE (all actors and templates), namespace $NAMESPACE, the workshop dashboard,"
  echo "and gs://$BUCKET_NAME/$ATESPACE/.$($ALL && echo " ALSO the bucket gs://$BUCKET_NAME, repo $AR_REPO, and their project IAM bindings.")"
  read -r -p "Type 'yes' to continue: " answer || answer=""
  [[ "$answer" == yes ]] || die "aborted"
fi
stop_port_forwards

if kubectl ate get atespace "$ATESPACE" >/dev/null 2>&1; then
  step "actors and templates in atespace $ATESPACE"
  for a in $(actor_names "$ATESPACE"); do kubectl ate delete actor "$a" -a "$ATESPACE" --any-state || warn "actor $a"; done
  for t in agent-v1 agent-v2 agent-v3; do kubectl ate delete actor-template "$t" -a "$ATESPACE" 2>/dev/null || true; done
  kubectl ate delete atespace "$ATESPACE" || warn "atespace $ATESPACE (delete remaining records and retry)"
fi

step "namespace $NAMESPACE (WorkerPool, gateway, Prometheus UI)"
kubectl -n "$NAMESPACE" delete workerpool agents --ignore-not-found --wait=true
kubectl delete ns "$NAMESPACE" --ignore-not-found

step "workshop dashboard"
for d in $(gcloud monitoring dashboards list --project "$PROJECT_ID" \
    --filter='displayName="Agent Substrate Workshop"' --format='value(name)'); do
  gcloud monitoring dashboards delete "$d" --project "$PROJECT_ID" --quiet
done

step "snapshots"
gcloud storage rm -r "gs://$BUCKET_NAME/$ATESPACE/**" --quiet 2>/dev/null || true

if $ALL; then
  step "bucket, Artifact Registry repo, and their project IAM bindings (undoing 'make gcp')"
  gcloud storage rm -r "gs://$BUCKET_NAME" --quiet || true
  gcloud artifacts repositories delete "$AR_REPO" --location "$REGION" --project "$PROJECT_ID" --quiet || true
  # Deleting the bucket also drops its bucket-level IAM policy. The project-level bindings
  # `scripts/gcp-resources.sh` created (Workload Identity -> Artifact Registry/Vertex/Monitoring)
  # outlive both the bucket and the repo, so they need to be removed explicitly.
  PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')" || true
  if [[ -n "${PROJECT_NUMBER:-}" ]]; then
    WI="principal://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject"
    COMPUTE_SA="serviceAccount:${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"
    unbind_project() { gcloud projects remove-iam-policy-binding "$PROJECT_ID" --member="$1" --role="$2" --condition=None --quiet >/dev/null 2>&1 || true; }
    for sa in atelet ate-api-server; do
      unbind_project "${WI}/ns/ate-system/sa/${sa}" roles/artifactregistry.reader
    done
    unbind_project "$COMPUTE_SA" roles/artifactregistry.reader
    unbind_project "${WI}/ns/${NAMESPACE}/sa/gateway" roles/aiplatform.user
    unbind_project "${WI}/ns/${NAMESPACE}/sa/prometheus-ui" roles/monitoring.viewer
    ok "project IAM bindings removed"
  else
    warn "could not resolve the project number; leaving project IAM bindings in place"
  fi
fi
rm -f "$STATE_DIR"/{agent-image,gateway-image,ate-token,ate-ca.pem,agent-v*.yaml}
ok "teardown complete (Substrate and the cluster are untouched)"
