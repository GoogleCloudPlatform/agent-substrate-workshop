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

# observability.sh: renders and applies Google Cloud Managed Service for Prometheus
# PodMonitoring/sre-gateway in ${NAMESPACE} and prints Cloud Trace / Monitoring links.
# Zero cmd/agent or ActorTemplate rebuilds required.
# shellcheck source=scripts/lib.sh
source "$(dirname "$0")/lib.sh"
load_env

step "Configuring Module 06 observability (GMP PodMonitoring + Cloud Trace waterfall)"

kubectl -n "$NAMESPACE" wait --for=condition=Available deploy/sre-gateway --timeout=60s >/dev/null
ok "deploy/sre-gateway available in $NAMESPACE"

"$REPO_ROOT/scripts/render.sh" \
  "$REPO_ROOT/modules/06-observability-and-evals/manifests/podmonitoring.yaml.tmpl" \
  >"$STATE_DIR/podmonitoring.yaml"

if kubectl get crd podmonitorings.monitoring.googleapis.com >/dev/null 2>&1; then
  kubectl apply -f "$STATE_DIR/podmonitoring.yaml"
  ok "applied PodMonitoring/sre-gateway (scraping /metrics every 15s)"
else
  warn "CRD podmonitorings.monitoring.googleapis.com not present on cluster; rendered $STATE_DIR/podmonitoring.yaml"
fi

state_put observability-enabled true
ok "Cloud Trace Explorer: https://console.cloud.google.com/traces/list?project=${PROJECT_ID}"
ok "Metrics Explorer:     https://console.cloud.google.com/monitoring/metrics-explorer?project=${PROJECT_ID}"
