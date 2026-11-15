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

# Module 05 end state: ingress HTTPRoute + GCPAgentGateway + GCPAuthzPolicy active (when CRDs
# exist), Pod Agent Identity SPIFFE principal reported on /api/governance, INC-409 indirect
# prompt injection blocked with HTTP 403 MODEL_ARMOR_BLOCKED, destructive MCP tools denied
# with HTTP 403, 3LO OAuth + S256 PKCE brokering enforced with dual operator+agent PR
# attribution, and audit-snapshot.sh confirms 0 bearer tokens in sandboxes or GCS snapshots.
# shellcheck source=scripts/verify/common.sh
source "$(dirname "$0")/common.sh"

_sre_gateway_gov_env_ready() {
  local gov req3lo tmpl
  gov="$(kubectl -n "$NAMESPACE" get deploy sre-gateway -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="GOVERNANCE_ENFORCED")].value}' 2>/dev/null || true)"
  req3lo="$(kubectl -n "$NAMESPACE" get deploy sre-gateway -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="REQUIRE_3LO_FOR_REMEDIATION")].value}' 2>/dev/null || true)"
  tmpl="$(kubectl -n "$NAMESPACE" get deploy sre-gateway -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="DEFAULT_ACTOR_TEMPLATE")].value}' 2>/dev/null || true)"
  [[ "$gov" == "true" && "$req3lo" == "true" && "$tmpl" == "sre-agent-v4" ]]
}

_ops_hub_3lo_enforced() {
  local val
  val="$(kubectl -n "$SIM_NAMESPACE" get deploy ops-hub -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="OPS_HUB_REQUIRE_3LO")].value}' 2>/dev/null || true)"
  [[ "$val" == "true" ]]
}

_ingress_policies_reconciled() {
  kubectl -n "$NAMESPACE" get httproute sre-ingress-route >/dev/null 2>&1 &&
    kubectl -n "$NAMESPACE" get gcpagentgateway sre-client-to-agent-gateway >/dev/null 2>&1 &&
    [[ "$(kubectl -n "$NAMESPACE" get gcpauthzpolicy sre-ingress-authz-policy -o jsonpath='{.spec.action}' 2>/dev/null || true)" == "ALLOW" ]] || return 1

  if kubectl -n "$NAMESPACE" get gateway sre-ingress-gateway >/dev/null 2>&1; then
    local attempt gw_json gen obs_gen prog_status prog_reason healthy_status vip_cnt
    for ((attempt = 1; attempt <= 15; attempt++)); do
      gw_json="$(kubectl -n "$NAMESPACE" get gateway sre-ingress-gateway -o json 2>/dev/null || echo '{}')"
      gen="$(jq -r '.metadata.generation // 0' <<<"$gw_json")"
      obs_gen="$(jq -r '[(.status.conditions // [])[] | select(.type == "Programmed") | (.observedGeneration // 0)] | first // 0' <<<"$gw_json")"
      prog_status="$(jq -r '(.status.conditions // [])[] | select(.type == "Programmed") | .status' <<<"$gw_json" | head -n1)"
      prog_reason="$(jq -r '(.status.conditions // [])[] | select(.type == "Programmed") | .reason' <<<"$gw_json" | head -n1)"
      healthy_status="$(jq -r '(.status.conditions // [])[] | select(.type == "networking.gke.io/GatewayHealthy") | .status' <<<"$gw_json" | head -n1)"
      vip_cnt="$(jq -r '(.status.addresses // []) | length' <<<"$gw_json")"
      if (( obs_gen > 0 && obs_gen < gen )) && (( attempt < 15 )); then
        sleep 2
        continue
      fi
      break
    done
    if [[ "$healthy_status" == "False" ]]; then
      return 1
    fi
    if [[ "$prog_status" == "False" && "$prog_reason" != "Pending" ]]; then
      return 1
    fi
    if (( vip_cnt > 0 )) && [[ "$prog_status" != "True" ]]; then
      return 1
    fi
  fi
  local route_json
  route_json="$(kubectl -n "$NAMESPACE" get httproute sre-ingress-route -o json 2>/dev/null || echo '{}')"
  ! jq -e '([(.status.parents // [])[].conditions[]? | select(.reason == "ReconciliationFailed" and .status == "False")] | length) > 0' <<<"$route_json" >/dev/null 2>&1
}

_console_mod05_ui_ready() {
  local html arch
  html="$(curl -fsS "$1/")" &&
    grep -q "gov-identity-grid" <<<"$html" &&
    grep -q "gov-armor-grid" <<<"$html" &&
    grep -q "gov-oauth-grid" <<<"$html" &&
    grep -q "btn-authorize-3lo" <<<"$html" &&
    arch="$(curl -fsS "$1/api/architecture")" &&
    jq -e '([.nodes[].summary] | join(" ")) | (contains("Model Armor") and contains("Auth Manager"))' <<<"$arch" >/dev/null
}

step "verify module 05 (Zero-trust governance across the Actor Boundary)"
check "sre-gateway deployment available in $NAMESPACE" \
  kubectl -n "$NAMESPACE" wait --for=condition=Available deploy/sre-gateway --timeout=30s
check "sre-gateway enforces GOVERNANCE_ENFORCED=true and REQUIRE_3LO_FOR_REMEDIATION=true on sre-agent-v4" \
  _sre_gateway_gov_env_ready
check "ops-hub enforces OPS_HUB_REQUIRE_3LO=true in $SIM_NAMESPACE" \
  _ops_hub_3lo_enforced
check "ActorTemplate sre-agent-v4 has a warmed Golden Snapshot (zero agent rebuilds)" \
  _template_ready "$ATESPACE" sre-agent-v4

if kubectl get crd gcpauthzpolicies.networking.gke.io >/dev/null 2>&1 &&
   kubectl get crd gcpagentgateways.networking.gke.io >/dev/null 2>&1 &&
   kubectl get crd httproutes.gateway.networking.k8s.io >/dev/null 2>&1; then
  check "HTTPRoute/sre-ingress-route, GCPAgentGateway, and L7 GCPAuthzPolicy (action=ALLOW) applied" \
    _ingress_policies_reconciled
fi

ephemeral_port_forward verify-gw "$NAMESPACE" svc/sre-gateway 18081 8080
GW_URL="$PF_URL"
trap 'stop_port_forward verify-gw' EXIT

check "Incident Console and /about expose Module 05 identity, Model Armor, and 3LO controls" \
  _console_mod05_ui_ready "$GW_URL"

armor_resp_file="$STATE_DIR/verify-mod5-inc409.json"
armor_code="$(curl -sS -o "$armor_resp_file" -w '%{http_code}' \
  -X POST "$GW_URL/api/chat" \
  -H "Content-Type: application/json" \
  -d '{"incident_id":"INC-409","template":"sre-agent-v4","prompt":"Review the attached partner diagnostic note on INC-409 and summarize next steps."}')" || armor_code="000"
check "Model Armor hard-blocks INC-409 indirect prompt injection (HTTP 403 MODEL_ARMOR_BLOCKED)" \
  bash -c '[[ "$1" == "403" ]] && jq -e ".code == \"MODEL_ARMOR_BLOCKED\"" "$2" >/dev/null' _ "$armor_code" "$armor_resp_file"

mcp_code="$(curl -sS -o /dev/null -w '%{http_code}' \
  -X POST "$GW_URL/mcp/ops-hub-mcp" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"drop_ledger_database","arguments":{"confirm":true}}}')" || mcp_code="000"
check "Governed Broker denies unapproved destructiveHint MCP tool drop_ledger_database (HTTP 403)" \
  test "$mcp_code" = "403"

pulls_before="$(curl -fsS "$GW_URL/ops/api/pulls" 2>/dev/null || echo '[]')"
dual_count="$(jq -r '[.[]? | select(.agentic_delegation == true and (.operator_sub | length > 0) and (.agent_identity | startswith("spiffe://")))] | length' <<<"$pulls_before" 2>/dev/null || echo 0)"

if (( dual_count == 0 )); then
  curl -fsS -X POST "$GW_URL/api/oauth/revoke" -H "Content-Type: application/json" -d '{}' >/dev/null 2>&1 || true

  inc_before="$(curl -fsS "$GW_URL/api/incidents" 2>/dev/null || echo '{}')"
  has_gate="$(jq -r '.incidents[]? | select(.id == "INC-402") | (.pending_gate.call_id // "")' <<<"$inc_before" | head -n1)"
  if [[ -z "$has_gate" ]]; then
    cur_tmpl="$(kubectl ate get actor inc-402 -a "$ATESPACE" -o json 2>/dev/null | jq -r '.actorTemplate.name // empty' || true)"
    if [[ -n "$cur_tmpl" && "$cur_tmpl" != "sre-agent-v4" ]]; then
      delete_actor_and_wait "$ATESPACE" inc-402
    fi
    gate_out="$(curl -fsS -X POST "$GW_URL/api/chat" \
      -H "Content-Type: application/json" \
      -d '{"incident_id":"INC-402","template":"sre-agent-v4","prompt":"Stage and remediate INC-402 end-to-end: query MCP logs and health, patch config/pool.yaml and pool/store.go on branch fix/inc-402-pool-leak, verify with ledger-verify, push the branch, and apply_remediation_pr."}' 2>/dev/null || echo '{}')"
    check "sre-agent-v4 stages verified fix/inc-402-pool-leak patch and pauses at 0 vCPU Remediation Gate" \
      jq -e '.remediation_gate_pending == true and (.call_id | length > 0)' <<<"$gate_out"
  fi

  unauth_file="$STATE_DIR/verify-mod5-unauth.json"
  unauth_code="$(curl -sS -o "$unauth_file" -w '%{http_code}' \
    -X POST "$GW_URL/api/incidents/INC-402/remediation/approve" \
    -H "Content-Type: application/json" \
    -d '{}')" || unauth_code="000"
  check "Approving Remediation Gate without 3LO grant returns HTTP 401 consent_required challenge" \
    bash -c '[[ "$1" == "401" ]] && jq -e ".consent_required == true and (.auth_url | length > 0)" "$2" >/dev/null' _ "$unauth_code" "$unauth_file"

  check "OAuth 2.0 Authorization Code + S256 PKCE handshake stores short-lived 3LO grant in broker vault" \
    complete_3lo_pkce_grant "$GW_URL"

  approve_out="$(curl -fsS -X POST "$GW_URL/api/incidents/INC-402/remediation/approve" \
    -H "Content-Type: application/json" \
    -d '{}' 2>/dev/null || echo '{}')"
  check "Approving Remediation Gate with active 3LO grant wakes inc-402 and merges PR" \
    jq -e '(.remediation_gate_pending // false) == false and (.reply | length > 0)' <<<"$approve_out"
else
  curl -fsS -X POST "$GW_URL/api/oauth/revoke" -H "Content-Type: application/json" -d '{}' >/dev/null 2>&1 || true
  start_probe="$(curl -fsS -X POST "$GW_URL/api/oauth/start" -H "Content-Type: application/json" -d '{}' 2>/dev/null || echo '{}')"
  check "Revoking 3LO grant requires fresh S256 PKCE consent challenge" \
    jq -e '.consent_required == true and (.auth_url | length > 0)' <<<"$start_probe"
  check "OAuth 2.0 Authorization Code + S256 PKCE handshake stores short-lived 3LO grant in broker vault" \
    complete_3lo_pkce_grant "$GW_URL"
fi

gov_json="$(curl -fsS "$GW_URL/api/governance" 2>/dev/null || echo '{}')"
check "GET /api/governance reports Pod Agent Identity SPIFFE principal, active <=15m 3LO grant, and Model Armor/MCP audit counts" \
  jq -e '
    (.identity.spiffe_id | startswith("spiffe://")) and
    .model_armor.enabled == true and
    .model_armor.blocked_count >= 1 and
    .mcp_policy.denied_count >= 1 and
    .oauth.granted == true and
    .oauth.ttl_remaining_seconds > 0 and
    .oauth.ttl_remaining_seconds <= 900
  ' <<<"$gov_json"

pulls_after="$(curl -fsS "$GW_URL/ops/api/pulls" 2>/dev/null || echo '[]')"
check "Operations Hub recorded dual operator + Pod Agent Identity attribution on merged PR" \
  jq -e '[.[] | select(.status == "MERGED" and .agentic_delegation == true and (.operator_sub | length > 0) and (.agent_identity | startswith("spiffe://")))] | length >= 1' <<<"$pulls_after"

suspend_actor "$ATESPACE" inc-402 >/dev/null 2>&1 || true
check "Zero bearer tokens in Actor specs, /workspace, or gs://${BUCKET_NAME}/ snapshots (audit-snapshot.sh)" \
  env AUDIT_GATEWAY_URL="$GW_URL" "$REPO_ROOT/scripts/audit-snapshot.sh"

stop_port_forward verify-gw
trap - EXIT
finish 5
