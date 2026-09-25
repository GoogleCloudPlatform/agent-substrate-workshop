#!/usr/bin/env bash
# Creates the GCP resources Substrate and the workshop need. Safe to re-run.
# Mirrors substrate's tools/setup-gcp (enable apis / create bucket / create iam) plus workshop grants.
source "$(dirname "$0")/lib.sh"
load_env
PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)')"
WI="principal://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject"
COMPUTE_SA="serviceAccount:${PROJECT_NUMBER}-compute@developer.gserviceaccount.com"

step "Enable APIs"
gcloud services enable --project "$PROJECT_ID" \
  container.googleapis.com storage.googleapis.com artifactregistry.googleapis.com \
  aiplatform.googleapis.com monitoring.googleapis.com logging.googleapis.com cloudtrace.googleapis.com
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

bind_bucket()  { gcloud storage buckets add-iam-policy-binding "gs://${BUCKET_NAME}" --member="$1" --role="$2" --condition=None >/dev/null; }
bind_project() { gcloud projects add-iam-policy-binding "$PROJECT_ID" --member="$1" --role="$2" --condition=None >/dev/null; }
# Artifact Registry read access is granted on the workshop repo only, not the project: least privilege,
# and deleting the repo (teardown --all) removes the binding with it, so nobody else's grant is touched.
# (If you point KO_DOCKER_REPO at a different repo, grant the same readers on that one.)
bind_repo()    { gcloud artifacts repositories add-iam-policy-binding "$AR_REPO" --location "$REGION" --project "$PROJECT_ID" --member="$1" --role="$2" >/dev/null; }

step "IAM for Substrate (Workload Identity principals in ate-system)"
for sa in atelet ate-api-server; do
  bind_bucket  "${WI}/ns/ate-system/sa/${sa}" roles/storage.objectAdmin
  bind_bucket  "${WI}/ns/ate-system/sa/${sa}" roles/storage.bucketViewer
  bind_repo    "${WI}/ns/ate-system/sa/${sa}" roles/artifactregistry.reader
done
bind_bucket  "$COMPUTE_SA" roles/storage.objectViewer
bind_repo    "$COMPUTE_SA" roles/artifactregistry.reader
ok "substrate IAM bound"

step "IAM for the workshop (gateway -> Vertex, prometheus-ui -> Monitoring)"
bind_project "${WI}/ns/${NAMESPACE}/sa/gateway" roles/aiplatform.user
bind_project "${WI}/ns/${NAMESPACE}/sa/prometheus-ui" roles/monitoring.viewer
ok "workshop IAM bound (propagation can take ~2 minutes)"
