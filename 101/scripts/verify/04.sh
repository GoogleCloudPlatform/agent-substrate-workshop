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
backend_ok() { jq -e '.backend | IN("gemini", "mock")' <<<"$body"; }
check "reply came from gemini (or from the mock, with MOCK_LLM=true)" backend_ok
# mock-fallback means the agent -> egress -> gateway path works, but the gateway's model call failed.
if [[ "$(jq -r '.backend // ""' <<<"$body" 2>/dev/null)" == mock-fallback ]]; then
  warn "the gateway answered from mock-fallback: its Gemini call failed. See why: curl -s localhost:${GATEWAY_LOCAL_PORT:-8081}/v1/model -d '{\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}' | jq .upstreamError (or kubectl -n $NAMESPACE logs deploy/gateway), and instructor/troubleshooting.md. To carry on with the mock: MOCK_LLM=true make gateway"
fi
finish 4
