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

# Module 6: scrape the gateway with Managed Prometheus and run a Prometheus UI over Cloud Monitoring.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_env
: "${PROM_FRONTEND_IMAGE:=gke.gcr.io/prometheus-engine/frontend:v0.15.3-gke.0}"
export PROM_FRONTEND_IMAGE
kubectl -n "$NAMESPACE" get deploy gateway >/dev/null 2>&1 || die "gateway not deployed — run: make gateway (Module 4) or make catch-up MODULE=5"
step "PodMonitoring for the gateway"
# Applied idempotently here too (gateway.sh already applies it once the gateway exists), so
# a re-run of this script — or a fresh shell that skips straight to Module 6 — stays correct.
kubectl -n "$NAMESPACE" apply -f "$REPO_ROOT/observability/podmonitoring.yaml"
step "Prometheus UI (GMP frontend)"
render "$REPO_ROOT/observability/prometheus-ui.yaml.tmpl" NAMESPACE PROJECT_ID PROM_FRONTEND_IMAGE | kubectl apply -f -
kubectl -n "$NAMESPACE" rollout status deploy/prometheus-ui --timeout=180s
ok "run: make prometheus-ui   (then $(open_hint 9090))"
