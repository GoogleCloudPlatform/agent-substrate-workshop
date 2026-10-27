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

# Renders and applies the gateway. Keeps a live IDLE_AFTER (set in Module 5) and a live MOCK_LLM
# (e.g. `kubectl set env … MOCK_LLM=true`) unless you pass one: IDLE_AFTER=… / MOCK_LLM=… make gateway.
source "$(dirname "$0")/lib.sh"
load_env
require_state gateway-image "make gateway"
GATEWAY_IMAGE="$(state_get gateway-image)"
export GATEWAY_IMAGE
: "${GEMINI_MODEL:=gemini-3.8-flash}" "${VERTEX_LOCATION:=${REGION:-us-central1}}"
if [[ -z "${IDLE_AFTER:-}" ]]; then
  IDLE_AFTER="$(kubectl -n "$NAMESPACE" get deploy gateway \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="IDLE_AFTER")].value}' 2>/dev/null || true)"
fi
export IDLE_AFTER="${IDLE_AFTER:-0s}" GEMINI_MODEL VERTEX_LOCATION
# load_env already let a command-line MOCK_LLM win over workshop.env; without one, the live value wins.
if [[ -z "${WORKSHOP_MOCK_LLM_OVERRIDE:-}" ]]; then
  live_mock="$(kubectl -n "$NAMESPACE" get deploy gateway \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="MOCK_LLM")].value}' 2>/dev/null || true)"
  if [[ -n "$live_mock" && "$live_mock" != "$MOCK_LLM" ]]; then
    warn "keeping the gateway's live MOCK_LLM=$live_mock (workshop.env says $MOCK_LLM); to change it: MOCK_LLM=$MOCK_LLM make gateway"
    MOCK_LLM="$live_mock"
  fi
fi
export MOCK_LLM
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl label namespace "$NAMESPACE" ate.dev/api-access=true --overwrite >/dev/null
ensure_gateway_access "$ATESPACE" "$NAMESPACE"
step "deploying gateway ($GATEWAY_IMAGE, MOCK_LLM=$MOCK_LLM, IDLE_AFTER=$IDLE_AFTER)"
render "$REPO_ROOT/modules/04-make-it-an-agent/manifests/gateway.yaml.tmpl" \
  NAMESPACE GATEWAY_IMAGE ATESPACE MOCK_LLM GEMINI_MODEL PROJECT_ID VERTEX_LOCATION IDLE_AFTER | kubectl apply -f -
kubectl -n "$NAMESPACE" rollout status deploy/gateway --timeout=180s
# A forward to a replaced pod can stay alive but dead; drop it so the next port_forward starts fresh.
if [[ -f "$STATE_DIR/pf-gateway.pid" ]]; then
  stop_port_forward gateway
  ok "stopped the old gateway port-forward (the pods were replaced); run make forward-gateway again if you use it"
fi
# The gateway's PodMonitoring (Task 15) may not exist yet; apply it here too so gateway
# metrics are scraped from the moment the gateway exists, not just from Module 6 onward.
if [[ -f "$REPO_ROOT/observability/podmonitoring.yaml" ]]; then
  step "applying gateway PodMonitoring (observability/podmonitoring.yaml)"
  kubectl -n "$NAMESPACE" apply -f "$REPO_ROOT/observability/podmonitoring.yaml"
fi
ok "gateway ready: http://gateway.$NAMESPACE.svc.cluster.local"
