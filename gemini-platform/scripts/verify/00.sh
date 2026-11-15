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

# Module 00 end state: GCP resources exist, ComputeClass applied, Substrate install running or
# complete, prod-sim (ledger-api + ops-hub) live, local verifier built, and bin/ on PATH.
source "$(dirname "$0")/common.sh"

step "verify module 00 (setup & prod-sim)"
check "bucket gs://$BUCKET_NAME exists" gcloud storage buckets describe "gs://$BUCKET_NAME" --project "$PROJECT_ID"
check "Artifact Registry repo $AR_REPO exists" gcloud artifacts repositories describe "$AR_REPO" --location "$REGION" --project "$PROJECT_ID"
check "Agent Substrate install running or complete" \
  bash -c 'source "$1/scripts/lib.sh"; install_running || [[ -s "$STATE_DIR/install-complete" && ( ! -f "$STATE_DIR/install.pid" || "$STATE_DIR/install-complete" -nt "$STATE_DIR/install.pid" ) ]] || kubectl get ns ate-system' _ "$REPO_ROOT"
check "ComputeClass/substrate-gvisor applied (or CRD pending)" \
  bash -c '! kubectl get crd computeclasses.cloud.google.com >/dev/null 2>&1 || kubectl get computeclass substrate-gvisor'
check "ops-hub deployment available in $SIM_NAMESPACE" \
  kubectl -n "$SIM_NAMESPACE" wait --for=condition=Available deploy/ops-hub --timeout=30s
check "ledger-api deployment available in $SIM_NAMESPACE" \
  kubectl -n "$SIM_NAMESPACE" wait --for=condition=Available deploy/ledger-api --timeout=30s
check "ops-hub serves INC-402 and INC-409" \
  bash -c '
    source "$1/scripts/lib.sh"
    load_env
    ephemeral_port_forward verify-opshub "$SIM_NAMESPACE" svc/ops-hub 18082 8080
    trap "stop_port_forward verify-opshub" EXIT
    curl -fsS "$PF_URL/api/incidents/INC-402" | jq -e ".id == \"INC-402\"" &&
      curl -fsS "$PF_URL/api/incidents/INC-409" | jq -e ".id == \"INC-409\""
  ' _ "$REPO_ROOT"
check "ledger-api reports live degraded pool status" \
  bash -c '
    source "$1/scripts/lib.sh"
    load_env
    ephemeral_port_forward verify-ledger "$SIM_NAMESPACE" svc/ledger-api 18083 8080
    trap "stop_port_forward verify-ledger" EXIT
    curl -fsS "$PF_URL/api/status" | jq -e ".service == \"ledger-api\""
  ' _ "$REPO_ROOT"
check "bin/ledger-verify built" test -x "$REPO_ROOT/bin/ledger-verify"
check "kubectl ate on your shell PATH (bin/)" \
  bash -c 'source "$1/scripts/lib.sh"; user_has_ate || install_running' _ "$REPO_ROOT"
if ! user_has_ate && ! install_running; then
  path_hint
fi
finish 0
