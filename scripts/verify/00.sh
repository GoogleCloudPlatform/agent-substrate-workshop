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
# Scripts get bin/ on PATH from lib.sh; your shell doesn't. Module 2 onwards you type `kubectl ate …`.
check "kubectl ate on your shell's PATH (bin/)" user_has_ate
user_has_ate || path_hint
finish 0
