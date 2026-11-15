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

# Module 04 end state: sre-gateway advertises /.well-known/agent-card.json + POST /a2a with
# GKE Agent Registry labels/annotations, /catalog exposes Agent Registry A2A & MCP servers
# with readOnlyHint/destructiveHint annotations, and sre-agent-v4 suspends to 0 vCPU at the
# Remediation Gate before resuming (< 400ms) on approval to resolve INC-402 and restore
# ledger-api /healthz to 200 OK.
# shellcheck source=scripts/verify/common.sh
source "$(dirname "$0")/common.sh"

_sre_gateway_agent_registry_metadata() {
  local ftype card_ann
  ftype="$(kubectl -n "$NAMESPACE" get deploy sre-gateway -o jsonpath='{.metadata.labels.registry\.gke\.io/functional-type}' 2>/dev/null || true)"
  card_ann="$(kubectl -n "$NAMESPACE" get deploy sre-gateway -o jsonpath='{.metadata.annotations.a2a-protocol\.org/agent-card}' 2>/dev/null || true)"
  [[ "$ftype" == "AGENT" && "$card_ann" == *"/.well-known/agent-card.json"* ]]
}

_catalog_mod04_ui_ready() {
  local html
  html="$(curl -fsS "$1/catalog")" &&
    grep -q "a2a-agents-body" <<<"$html" &&
    grep -q "mcp-servers-body" <<<"$html" &&
    grep -q "gate-banner" <<<"$html"
}

step "verify module 04 (Agent Registry discovery & zero-compute Remediation Gate)"
check "sre-gateway deployment available in $NAMESPACE" \
  kubectl -n "$NAMESPACE" wait --for=condition=Available deploy/sre-gateway --timeout=30s
check "sre-gateway carries GKE Agent Registry functional-type=AGENT label and A2A agent-card annotation" \
  _sre_gateway_agent_registry_metadata
check "ActorTemplate sre-agent-v4 has a warmed Golden Snapshot" \
  _template_ready "$ATESPACE" sre-agent-v4

ephemeral_port_forward verify-gw "$NAMESPACE" svc/sre-gateway 18081 8080
GW_URL="$PF_URL"
trap 'stop_port_forward verify-gw' EXIT

card_json="$(curl -fsS "$GW_URL/.well-known/agent-card.json" 2>/dev/null || true)"
check "GET /.well-known/agent-card.json serves Platform SRE Triage Agent card" \
  jq -e '.name == "Platform SRE Triage Agent" and (.url | endswith("/a2a")) and (.skills | length >= 2)' <<<"$card_json"

check "Incident Console /catalog view serves A2A, MCP, and Remediation Gate elements" \
  _catalog_mod04_ui_ready "$GW_URL"

cat_json="$(curl -fsS "$GW_URL/api/catalog" 2>/dev/null || true)"
check "Agent Registry catalog exposes logging-mcp, container-mcp, and ops-hub-mcp with tool annotations" \
  jq -e '([.mcp_servers[].mcpServerId] | contains(["logging-mcp", "container-mcp", "ops-hub-mcp"])) and ([.mcp_servers[].tools[].annotations.readOnlyHint] | any) and ([.mcp_servers[].tools[].annotations.destructiveHint] | any)' <<<"$cat_json"

a2a_json="$(curl -fsS -X POST "$GW_URL/a2a" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":"verify-a2a-1","method":"message/send","params":{"contextId":"inc-403","metadata":{"incident_id":"INC-403"},"message":{"role":"user","parts":[{"kind":"text","text":"Triage sibling incident INC-403 using Consolidated Memory from prior ledger-api incidents."}]}}}' 2>/dev/null || true)"
check "POST /a2a JSON-RPC 2.0 message/send completes governed triage task" \
  jq -e '.jsonrpc == "2.0" and .result.status.state == "completed" and (.result.parts | length > 0)' <<<"$a2a_json"

inc_before="$(curl -fsS "$GW_URL/api/incidents" 2>/dev/null || echo '{}')"
inc402_status="$(jq -r '.incidents[]? | select(.id == "INC-402") | .status' <<<"$inc_before" | head -n1)"
has_pending_gate="$(jq -r '.incidents[]? | select(.id == "INC-402") | (.pending_gate.call_id // "")' <<<"$inc_before" | head -n1)"

if [[ "$inc402_status" != "RESOLVED" ]]; then
  if [[ -z "$has_pending_gate" ]]; then
    cur_tmpl="$(kubectl ate get actor inc-402 -a "$ATESPACE" -o json 2>/dev/null | jq -r '.actorTemplate.name // empty' || true)"
    if [[ -n "$cur_tmpl" && "$cur_tmpl" != "sre-agent-v4" ]]; then
      delete_actor_and_wait "$ATESPACE" inc-402
    fi
    gate_turn="$(curl -fsS -X POST "$GW_URL/api/chat" \
      -H "Content-Type: application/json" \
      -d '{"incident_id":"INC-402","template":"sre-agent-v4","prompt":"Stage and remediate INC-402 end-to-end: query MCP logs and health, patch config/pool.yaml and pool/store.go on branch fix/inc-402-pool-leak, verify with ledger-verify, push the branch, and apply_remediation_pr."}' 2>/dev/null || true)"
    check "sre-agent-v4 stages verified fix/inc-402-pool-leak patch and pauses at Remediation Gate" \
      jq -e '.remediation_gate_pending == true and (.call_id | length > 0) and .workspace.verify_ok == true' <<<"$gate_turn"
  fi

  check "inc-402 is suspended at 0 vCPU in Cloud Storage while awaiting Remediation Gate approval" \
    _actor_suspended "$ATESPACE" inc-402

  approve_out="$(curl -fsS -X POST "$GW_URL/api/incidents/INC-402/remediation/approve" \
    -H "Content-Type: application/json" \
    -d '{}' 2>/dev/null || true)"
  check "Approving Remediation Gate wakes inc-402 and executes apply_remediation_pr" \
    jq -e '(.remediation_gate_pending // false) == false and (.reply | length > 0)' <<<"$approve_out"
fi

ledger_402="$(curl -fsS "$GW_URL/api/ledger/sessions/inc-402" 2>/dev/null || true)"
check "Session Ledger recorded adk_request_confirmation event for inc-402" \
  grep -q "adk_request_confirmation" <<<"$ledger_402"

gov_json="$(curl -fsS "$GW_URL/api/governance" 2>/dev/null || true)"
check "Governance policy log recorded Remediation Gate 0 vCPU suspension and approval" \
  jq -e '[.events[] | select(.boundary == "Remediation Gate")] | length >= 2' <<<"$gov_json"

inc_after="$(curl -fsS "$GW_URL/api/incidents" 2>/dev/null || true)"
check "INC-402 is RESOLVED via merged PR and ledger-api SLO is restored (200 OK)" \
  jq -e '([.incidents[] | select(.id == "INC-402" and .status == "RESOLVED")] | length == 1) and .target_status.slo_met == true' <<<"$inc_after"

stop_port_forward verify-gw
trap - EXIT
finish 4
