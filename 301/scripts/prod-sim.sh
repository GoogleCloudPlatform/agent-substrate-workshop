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

# Builds and deploys the live prod-sim incident environment (ledger-api Target Service + ops-hub Operations Hub).
source "$(dirname "$0")/lib.sh"
load_env
export KO_DOCKER_REPO KO_DEFAULTPLATFORMS

step "ko build ledger-api -> ${KO_DOCKER_REPO}"
ledger_ref="$(cd "$REPO_ROOT/app" && ko build --base-import-paths ./cmd/ledger-api)"
[[ "$ledger_ref" == *@sha256:* ]] || die "ko did not return a digest-pinned ref for ledger-api: $ledger_ref"
state_put ledger-api-image "$ledger_ref"
ok "$ledger_ref"

step "ko build ops-hub -> ${KO_DOCKER_REPO}"
opshub_ref="$(cd "$REPO_ROOT/app" && ko build --base-import-paths ./cmd/ops-hub)"
[[ "$opshub_ref" == *@sha256:* ]] || die "ko did not return a digest-pinned ref for ops-hub: $opshub_ref"
state_put ops-hub-image "$opshub_ref"
ok "$opshub_ref"

step "Deploy ${SIM_NAMESPACE} (ledger-api + ops-hub)"
"$REPO_ROOT/scripts/render.sh" "$REPO_ROOT/modules/00-setup/manifests/prod-sim.yaml.tmpl" | kubectl apply -f -
kubectl -n "$SIM_NAMESPACE" rollout status deploy/ops-hub deploy/ledger-api --timeout=180s
ok "prod-sim ready in namespace ${SIM_NAMESPACE}"
