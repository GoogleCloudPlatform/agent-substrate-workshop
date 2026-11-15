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

# Module 06 end state: GMP PodMonitoring/sre-gateway applied (when CRD exists), GET /metrics
# exposes low-cardinality ActorTemplate metrics with zero session/actor ID labels, W3C traceparent
# propagates across sre-gateway -> atenet-router -> Incident Actor -> /otlp/v1/traces with
# secret/payload scrubbing and gen_ai.* attributes, and pure-Go evalrunner passes all 4 rubrics.
# shellcheck source=scripts/verify/common.sh
source "$(dirname "$0")/common.sh"

_podmonitoring_ready() {
  local port path
  port="$(kubectl -n "$NAMESPACE" get podmonitoring sre-gateway -o jsonpath='{.spec.endpoints[0].port}' 2>/dev/null || true)"
  path="$(kubectl -n "$NAMESPACE" get podmonitoring sre-gateway -o jsonpath='{.spec.endpoints[0].path}' 2>/dev/null || true)"
  [[ "$port" == "http" && "$path" == "/metrics" ]]
}

_metrics_low_cardinality() {
  local m="$1"
  grep -q 'sre_gateway_turns_total{' <<<"$m" &&
    grep -q 'sre_gateway_otel_spans_exported_total{' <<<"$m" &&
    ! grep -qE 'session_id=|actor_id=|inc-402|inc-403|inc-409' <<<"$m"
}

_console_mod06_ui_ready() {
  local html arch
  html="$(curl -fsS "$1/")" &&
    grep -q 'btn-run-evals' <<<"$html" &&
    grep -q 'eval-rubrics-grid' <<<"$html" &&
    grep -q 'trace-waterfall-box' <<<"$html" &&
    arch="$(curl -fsS "$1/api/architecture")" &&
    jq -e '[.flows[].id] | index("otel-evals-and-scc") != null' <<<"$arch" >/dev/null
}

step "verify module 06 (Observability, GenAI traces, and online evals)"
check "sre-gateway deployment available in $NAMESPACE" \
  kubectl -n "$NAMESPACE" wait --for=condition=Available deploy/sre-gateway --timeout=30s
check "ActorTemplate sre-agent-v4 remains active with zero image rebuilds" \
  _template_ready "$ATESPACE" sre-agent-v4

if kubectl get crd podmonitorings.monitoring.googleapis.com >/dev/null 2>&1; then
  check "GMP PodMonitoring/sre-gateway scrapes http:/metrics in $NAMESPACE" \
    _podmonitoring_ready
fi

mkdir -p "$REPO_ROOT/bin"
(cd "$REPO_ROOT/app" && go build -o ../bin/evalrunner ./cmd/evalrunner)

ephemeral_port_forward verify-gw "$NAMESPACE" svc/sre-gateway 18081 8080
GW_URL="$PF_URL"
trap 'stop_port_forward verify-gw' EXIT

check "Incident Console and /about expose OpenTelemetry waterfall, online evals, and SCC flow" \
  _console_mod06_ui_ready "$GW_URL"

# Ensure sibling INC-403 session and W3C traceparent waterfall exist on the active sre-gateway pod.
verify_trace_id="4bf92f3577b34da6a3ce929d0e0e4736"
sib_code="$(curl -sS -o "$STATE_DIR/verify-mod6-403.json" -w '%{http_code}' \
  -X POST "$GW_URL/api/chat" \
  -H "Content-Type: application/json" \
  -H "traceparent: 00-${verify_trace_id}-00f067aa0ba902b7-01" \
  -d '{"incident_id":"INC-403","template":"sre-agent-v4","prompt":"Triage sibling incident INC-403 using Consolidated Memory from prior ledger-api incidents."}')" || sib_code="000"
check "W3C traceparent turn across sre-gateway -> atenet-router -> Incident Actor succeeded" \
  test "$sib_code" = "200"

trace_json="$(curl -fsS "$GW_URL/api/traces/${verify_trace_id}" 2>/dev/null || echo '{}')"
check "GET /api/traces/${verify_trace_id} links broker + actor spans with gen_ai.system and 0 leaked tokens" \
  jq -e '
    .trace_id == "4bf92f3577b34da6a3ce929d0e0e4736" and
    ([.spans[].name] | (index("invoke_gateway_turn") != null and index("atenet.wake_and_route") != null and index("invoke_agent") != null)) and
    (.spans[0].attributes["gen_ai.system"] == "gemini_enterprise_agent_platform") and
    ((tostring | test("ya29\\.|gho_|opshub_at_|gcp\\.vertex\\.agent\\.tool_call_args")) | not)
  ' <<<"$trace_json"

if [[ "${MOCK_LLM:-false}" != "true" ]]; then
  check "Cloud Trace v2 export succeeded for ${verify_trace_id} (exported_to_cloud_trace=true)" \
    jq -e '.exported_to_cloud_trace == true' <<<"$trace_json"
fi

check "Pure-Go online evals runner (bin/evalrunner) passes all 4 rubrics" \
  "$REPO_ROOT/bin/evalrunner" --gateway-url "$GW_URL" --min-score 0.80 --output "$STATE_DIR/eval-report.json"

check "Saved .workshop/eval-report.json contains 4 passing rubrics and overall_score >= 0.80" \
  jq -e '
    .passed == true and
    .overall_score >= 0.80 and
    (.rubrics | length == 4) and
    ([.rubrics[].passed] | all)
  ' "$STATE_DIR/eval-report.json"

metrics_body="$(curl -fsS "$GW_URL/metrics" 2>/dev/null || true)"
check "GET /metrics exposes ActorTemplate counters and online eval gauges without high-cardinality IDs" \
  _metrics_low_cardinality "$metrics_body"

suspend_actor "$ATESPACE" inc-403 >/dev/null 2>&1 || true
stop_port_forward verify-gw
trap - EXIT
finish 6
