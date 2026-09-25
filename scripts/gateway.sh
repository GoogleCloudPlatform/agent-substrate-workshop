#!/usr/bin/env bash
# Renders and applies the gateway. Keeps a live IDLE_AFTER (set in Module 5) unless you pass one.
source "$(dirname "$0")/lib.sh"
load_env
require_state gateway-image "make gateway"
GATEWAY_IMAGE="$(state_get gateway-image)"
export GATEWAY_IMAGE
: "${GEMINI_MODEL:=gemini-2.5-flash}" "${VERTEX_LOCATION:=${REGION:-us-central1}}"
if [[ -z "${IDLE_AFTER:-}" ]]; then
  IDLE_AFTER="$(kubectl -n "$NAMESPACE" get deploy gateway \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="IDLE_AFTER")].value}' 2>/dev/null || true)"
fi
export IDLE_AFTER="${IDLE_AFTER:-0s}" GEMINI_MODEL VERTEX_LOCATION
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
step "deploying gateway ($GATEWAY_IMAGE, MOCK_LLM=$MOCK_LLM, IDLE_AFTER=$IDLE_AFTER)"
render "$REPO_ROOT/modules/04-make-it-an-agent/manifests/gateway.yaml.tmpl" \
  NAMESPACE GATEWAY_IMAGE ATESPACE MOCK_LLM GEMINI_MODEL PROJECT_ID VERTEX_LOCATION IDLE_AFTER | kubectl apply -f -
kubectl -n "$NAMESPACE" rollout status deploy/gateway --timeout=180s
# The gateway's PodMonitoring (Task 15) may not exist yet; apply it here too so gateway
# metrics are scraped from the moment the gateway exists, not just from Module 6 onward.
if [[ -f "$REPO_ROOT/observability/podmonitoring.yaml" ]]; then
  step "applying gateway PodMonitoring (observability/podmonitoring.yaml)"
  kubectl -n "$NAMESPACE" apply -f "$REPO_ROOT/observability/podmonitoring.yaml"
fi
ok "gateway ready: http://gateway.$NAMESPACE.svc.cluster.local"
