#!/usr/bin/env bash
# Module 4 end state: gateway up, buddy has a gateway-only egress policy, and a chat turn returns a model reply.
source "$(dirname "$0")/common.sh"
step "verify module 4 (make it an agent)"
check "gateway is ready" kubectl -n "$NAMESPACE" rollout status deploy/gateway --timeout=10s
check "template agent-v3 has a golden snapshot" _template_ready "$ATESPACE" agent-v3
check "buddy has an EgressPolicy allowing the gateway" bash -c \
  "'$REPO_ROOT/scripts/egress.sh' get --actor=buddy | grep -q 'gateway.workshop.svc.cluster.local'"
out="$(gateway_chat buddy "verify: say ok" 2>&1)" || true
code="${out##*$'\n'}"; body="${out%$'\n'*}"
check "chat with buddy returns 200 (got $code)" test "$code" = 200
backend_ok() { jq -e '.backend | IN("vertex", "mock", "mock-fallback")' <<<"$body"; }
check "reply came from a model backend" backend_ok
# mock-fallback still passes (the agent -> egress -> gateway path works), but Vertex refused the call.
if [[ "$(jq -r '.backend // ""' <<<"$body" 2>/dev/null)" == mock-fallback ]]; then
  warn "the gateway answered from mock-fallback: Vertex refused the request. Check: kubectl -n $NAMESPACE logs deploy/gateway, and instructor/troubleshooting.md"
fi
finish 4
