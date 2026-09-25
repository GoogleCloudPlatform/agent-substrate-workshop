#!/usr/bin/env bash
# Module 0 end state: GCP resources exist, the install has started, dashboards exist, tools are built.
source "$(dirname "$0")/common.sh"
step "verify module 0 (setup)"
check "bucket gs://$BUCKET_NAME exists" gcloud storage buckets describe "gs://$BUCKET_NAME" --project "$PROJECT_ID"
check "Artifact Registry repo $AR_REPO exists" gcloud artifacts repositories describe "$AR_REPO" --location "$REGION" --project "$PROJECT_ID"
check "Substrate install started (.workshop/install.log or ate-system)" \
  bash -c "[[ -s '$STATE_DIR/install.log' ]] || kubectl get ns ate-system"
check "workshop dashboard exists" bash -c \
  "gcloud monitoring dashboards list --project '$PROJECT_ID' --filter='displayName=\"Agent Substrate Workshop\"' --format='value(name)' | grep -q ."
check "bin/egressctl built" test -x "$REPO_ROOT/bin/egressctl"
finish 0
