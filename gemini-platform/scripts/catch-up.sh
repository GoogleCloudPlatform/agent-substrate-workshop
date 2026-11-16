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

# catch-up.sh N: bring your environment to module N (MODULE=0 completes Module 00 setup;
# MODULE=1..7 brings the cluster to the start of module N / end of module N-1).
# Safe to re-run; skips steps that are already complete. With RESET=1, deletes and recreates
# the newest actor template staged by catch-up N from its completed solution.
# shellcheck source=scripts/lib.sh
source "$(dirname "$0")/lib.sh"
load_env

N="${1:-}"
[[ "$N" =~ ^[0-7]$ ]] || die "usage: make catch-up MODULE=<0-7> [RESET=1]"

RESET_TEMPLATE=""
if [[ "${RESET:-}" == 1 ]]; then
  if (( N < 2 )); then
    die "RESET=1 needs MODULE=2 or later: module 0 creates no actor template"
  fi
  case "$N" in
    2) RESET_TEMPLATE="sre-agent-v1" ;;
    3) RESET_TEMPLATE="sre-agent-v2" ;;
    4) RESET_TEMPLATE="sre-agent-v3" ;;
    *) RESET_TEMPLATE="sre-agent-v4" ;;
  esac
fi

S="$REPO_ROOT/scripts"
M="$REPO_ROOT/modules"
MAIN="$REPO_ROOT/app/cmd/agent/main.go"
BACKED_UP=false

is_known_solution() {
  local f
  for f in "$M"/*/solution/main.go; do
    [[ -f "$f" ]] && cmp -s "$f" "$MAIN" && return 0
  done
  return 1
}

use_solution() {
  local sol="$M/$1/solution/main.go"
  SOLUTION_CHANGED=false
  cmp -s "$sol" "$MAIN" && return 0
  SOLUTION_CHANGED=true
  if ! $BACKED_UP && ! is_known_solution; then
    local bak
    bak="$STATE_DIR/main.go.bak.$(date +%Y%m%d-%H%M%S).$$"
    cp "$MAIN" "$bak"
    BACKED_UP=true
    ok "backed up your app/cmd/agent/main.go to $bak"
  fi
  cp "$sol" "$MAIN"
  ok "app/cmd/agent/main.go <- modules/$1/solution/main.go"
}

stage_template() {
  local mod_slug="$1" tmpl_name="$2" tmpl_file="$3" target_n="${4:-0}"
  use_solution "$mod_slug"
  if [[ "$tmpl_name" == "$RESET_TEMPLATE" ]] || ( (( target_n == N )) && $SOLUTION_CHANGED && kubectl ate get actor-template "$tmpl_name" -a "$ATESPACE" >/dev/null 2>&1 ); then
    step "Rebuilding template $tmpl_name and resetting its actors from solution"
    reset_template "$ATESPACE" "$tmpl_name"
    ok "template $tmpl_name deleted"
  fi
  if ! kubectl ate get actor-template "$tmpl_name" -a "$ATESPACE" >/dev/null 2>&1; then
    "$S/build.sh" agent
    "$S/render.sh" "$M/$mod_slug/manifests/$tmpl_file" >"$STATE_DIR/$tmpl_name.yaml"
  fi
  ensure_template "$ATESPACE" "$tmpl_name" "$STATE_DIR/$tmpl_name.yaml"
  ok "template $tmpl_name ready with warmed Golden Snapshot"
}

_no_bg_install() { ! install_running && ! pgrep -f 'bash .*scripts/install-substrate.sh' >/dev/null; }
_two_sre_workers() {
  [[ "$(kubectl -n "$NAMESPACE" get pods -l workload=sre-agent --field-selector=status.phase=Running -o name 2>/dev/null | wc -l)" -ge 2 ]]
}

if (( N == 0 )); then
  step "Catching up module 00 setup (preflight, GCP resources, Agent Substrate, prod-sim, and local tools)"
else
  step "Catching up to the start of module $N (completing module 00 setup)"
fi
"$S/preflight.sh"
resolve_spiffe_identity
if [[ "$(state_get gcp-complete || true)" != *"PROJECT=$PROJECT_ID NAMESPACE=$NAMESPACE SPIFFE=$WORKLOAD_PRINCIPAL_PREFIX" ]] ||
   ! kubectl get ns "$NAMESPACE" >/dev/null 2>&1; then
  "$S/gcp-resources.sh"
fi
if ! _no_bg_install; then
  step "Waiting for background Agent Substrate install (.workshop/install.log)"
  wait_for 1800 "background install to finish" _no_bg_install
fi
if [[ "$(state_get install-complete || true)" != *"SUBSTRATE_REF=$SUBSTRATE_REF" ]]; then
  "$S/install-substrate.sh"
fi
make -s -C "$REPO_ROOT" tools
if [[ ! -s "$STATE_DIR/ledger-api-image" || ! -s "$STATE_DIR/ops-hub-image" ]] ||
   ! kubectl -n "$SIM_NAMESPACE" get deploy ops-hub ledger-api >/dev/null 2>&1; then
  "$S/prod-sim.sh"
fi
if (( N < 6 )); then
  rm -f "$STATE_DIR/governance-enforced" "$STATE_DIR/require-3lo"
  export GOVERNANCE_ENFORCED=false REQUIRE_3LO_FOR_REMEDIATION=false
  export WORKSHOP_GOV_OVERRIDE=false WORKSHOP_3LO_OVERRIDE=false
  if [[ "$(kubectl -n "$SIM_NAMESPACE" get deploy ops-hub -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="OPS_HUB_REQUIRE_3LO")].value}' 2>/dev/null || true)" == "true" ]]; then
    kubectl -n "$SIM_NAMESPACE" set env deploy/ops-hub OPS_HUB_REQUIRE_3LO=false >/dev/null
    kubectl -n "$SIM_NAMESPACE" rollout status deploy/ops-hub --timeout=120s >/dev/null
  fi
fi

if (( N >= 2 )); then
  step "Module 01 end state: WorkerPool/sre-workers, sre-gateway, sre-agent-v1, and INC-402 Turn 1"
  if kubectl -n "$NAMESPACE" get workerpool sre-workers >/dev/null 2>&1 &&
     [[ "$(kubectl -n "$NAMESPACE" get workerpool sre-workers -o jsonpath='{.spec.workerImage}' 2>/dev/null)" != "$(state_get worker-image)" ]]; then
    kubectl -n "$NAMESPACE" delete workerpool sre-workers --ignore-not-found --wait=true
  fi
  "$S/render.sh" "$M/01-actor-boundary/manifests/workerpool.yaml.tmpl" | kubectl apply -f -
  wait_for 600 "2 running sre-workers pods in $NAMESPACE" _two_sre_workers
  ensure_atespace "$ATESPACE"

  [[ -s "$STATE_DIR/gateway-image" ]] || "$S/build.sh" sre-gateway
  if (( N == 2 )); then
    WORKSHOP_TEMPLATE_OVERRIDE=sre-agent-v1 DEFAULT_ACTOR_TEMPLATE=sre-agent-v1 "$S/sre-gateway.sh"
  elif ! kubectl -n "$NAMESPACE" get deploy sre-gateway >/dev/null 2>&1; then
    "$S/sre-gateway.sh"
  fi

  stage_template "01-actor-boundary" "sre-agent-v1" "template-v1.yaml.tmpl" 2

  if (( N == 2 )); then
    ephemeral_port_forward catchup-gw "$NAMESPACE" svc/sre-gateway 18081 8080
    gw_url="$PF_URL"
    trap 'stop_port_forward catchup-gw' EXIT
    chat_code="$(curl -sS -o "$STATE_DIR/catchup-turn1.json" -w '%{http_code}' \
      -X POST "${gw_url}/api/chat" \
      -H "Content-Type: application/json" \
      -d '{"incident_id":"INC-402","template":"sre-agent-v1","prompt":"Investigate INC-402 by cloning ledger-api into /workspace, reading config/pool.yaml and pool/store.go, and running ledger-verify."}')" || chat_code="000"
    if [[ "$chat_code" != "200" ]]; then
      sleep 8
      chat_code="$(curl -sS -o "$STATE_DIR/catchup-turn1.json" -w '%{http_code}' \
        -X POST "${gw_url}/api/chat" \
        -H "Content-Type: application/json" \
        -d '{"incident_id":"INC-402","template":"sre-agent-v1","prompt":"Investigate INC-402 by cloning ledger-api into /workspace, reading config/pool.yaml and pool/store.go, and running ledger-verify."}')" || chat_code="000"
    fi
    [[ "$chat_code" == "200" ]] || die "INC-402 Turn 1 returned HTTP $chat_code; check kubectl -n $NAMESPACE logs deploy/sre-gateway"
    stop_port_forward catchup-gw
    trap - EXIT
    ok "INC-402 Turn 1 completed via sre-gateway"
  fi
fi

if (( N >= 3 )); then
  step "Module 02 end state: sre-agent-v2, Session Ledger, and Consolidated Memory"
  stage_template "02-sessions-and-memory" "sre-agent-v2" "template-v2.yaml.tmpl" 3

  if (( N == 3 )); then
    WORKSHOP_TEMPLATE_OVERRIDE=sre-agent-v2 DEFAULT_ACTOR_TEMPLATE=sre-agent-v2 "$S/sre-gateway.sh"
    ephemeral_port_forward catchup-gw "$NAMESPACE" svc/sre-gateway 18081 8080
    gw_url="$PF_URL"
    trap 'stop_port_forward catchup-gw' EXIT
    chat_code="$(curl -sS -o "$STATE_DIR/catchup-mod2-402.json" -w '%{http_code}' \
      -X POST "${gw_url}/api/chat" \
      -H "Content-Type: application/json" \
      -d '{"incident_id":"INC-402","template":"sre-agent-v2","prompt":"Investigate INC-402 by cloning ledger-api into /workspace, reading config/pool.yaml and pool/store.go, and running ledger-verify."}')" || chat_code="000"
    if [[ "$chat_code" != "200" ]]; then
      sleep 8
      chat_code="$(curl -sS -o "$STATE_DIR/catchup-mod2-402.json" -w '%{http_code}' \
        -X POST "${gw_url}/api/chat" \
        -H "Content-Type: application/json" \
        -d '{"incident_id":"INC-402","template":"sre-agent-v2","prompt":"Investigate INC-402 by cloning ledger-api into /workspace, reading config/pool.yaml and pool/store.go, and running ledger-verify."}')" || chat_code="000"
    fi
    [[ "$chat_code" == "200" ]] || die "Module 02 INC-402 turn returned HTTP $chat_code"

    sib_code="$(curl -sS -o "$STATE_DIR/catchup-mod2-403.json" -w '%{http_code}' \
      -X POST "${gw_url}/api/chat" \
      -H "Content-Type: application/json" \
      -d '{"incident_id":"INC-403","template":"sre-agent-v2","prompt":"Triage sibling incident INC-403 using Consolidated Memory from prior ledger-api incidents."}')" || sib_code="000"
    [[ "$sib_code" == "200" ]] || die "Module 02 INC-403 sibling recall turn returned HTTP $sib_code"

    suspend_actor "$ATESPACE" inc-402
    stop_port_forward catchup-gw
    trap - EXIT
    ok "Session Ledger and Consolidated Memory populated on sre-agent-v2"
  fi
fi

if (( N >= 4 )); then
  step "Module 03 end state: sre-agent-v3, SkillToolset, and Procedural Skill v1 -> v2 rollout"
  stage_template "03-skill-registry" "sre-agent-v3" "template-v3.yaml.tmpl" 4

  if (( N == 4 )); then
    WORKSHOP_TEMPLATE_OVERRIDE=sre-agent-v3 DEFAULT_ACTOR_TEMPLATE=sre-agent-v3 "$S/sre-gateway.sh"

    ephemeral_port_forward catchup-gw "$NAMESPACE" svc/sre-gateway 18081 8080
    gw_url="$PF_URL"
    trap 'stop_port_forward catchup-gw' EXIT

    REV=v1 BROKER_SYNC_URL="${gw_url}" "$S/publish-skill.sh"

    v1_code="$(curl -sS -o "$STATE_DIR/catchup-mod3-v1.json" -w '%{http_code}' \
      -X POST "${gw_url}/api/chat" \
      -H "Content-Type: application/json" \
      -d '{"incident_id":"INC-402","template":"sre-agent-v3","prompt":"Run the ledger-pool-triage procedural skill against INC-402 and report the SKILL_REVISION findings."}')" || v1_code="000"
    if [[ "$v1_code" != "200" ]]; then
      sleep 8
      v1_code="$(curl -sS -o "$STATE_DIR/catchup-mod3-v1.json" -w '%{http_code}' \
        -X POST "${gw_url}/api/chat" \
        -H "Content-Type: application/json" \
        -d '{"incident_id":"INC-402","template":"sre-agent-v3","prompt":"Run the ledger-pool-triage procedural skill against INC-402 and report the SKILL_REVISION findings."}')" || v1_code="000"
    fi
    [[ "$v1_code" == "200" ]] || die "Module 03 v1 skill turn returned HTTP $v1_code"

    REV=v2 BROKER_SYNC_URL="${gw_url}" "$S/publish-skill.sh"

    v2_code="$(curl -sS -o "$STATE_DIR/catchup-mod3-v2.json" -w '%{http_code}' \
      -X POST "${gw_url}/api/chat" \
      -H "Content-Type: application/json" \
      -d '{"incident_id":"INC-402","template":"sre-agent-v3","prompt":"Re-run the ledger-pool-triage procedural skill on INC-402 and report the updated SKILL_REVISION findings."}')" || v2_code="000"
    [[ "$v2_code" == "200" ]] || die "Module 03 v2 hot-reload turn returned HTTP $v2_code"
    suspend_actor "$ATESPACE" inc-402

    stop_port_forward catchup-gw
    trap - EXIT
  fi
  ok "Procedural Skill ledger-pool-triage (v1 & v2) active on sre-agent-v3"
fi

if (( N >= 5 )); then
  step "Module 04 end state: sre-agent-v4, Agent Registry MCP & A2A, and INC-402 Remediation Gate"
  stage_template "04-agent-registry-mcp-a2a" "sre-agent-v4" "template-v4.yaml.tmpl" 5

  if (( N == 5 )); then
    WORKSHOP_TEMPLATE_OVERRIDE=sre-agent-v4 DEFAULT_ACTOR_TEMPLATE=sre-agent-v4 "$S/sre-gateway.sh"
    ephemeral_port_forward catchup-gw "$NAMESPACE" svc/sre-gateway 18081 8080
    gw_url="$PF_URL"
    trap 'stop_port_forward catchup-gw' EXIT

    REV=v1 BROKER_SYNC_URL="${gw_url}" "$S/publish-skill.sh"
    REV=v2 BROKER_SYNC_URL="${gw_url}" "$S/publish-skill.sh"

    inc_state="$(curl -fsS "${gw_url}/api/incidents" 2>/dev/null || echo '{}')"
    inc402_status="$(jq -r '.incidents[]? | select(.id == "INC-402") | .status' <<<"$inc_state" | head -n1)"

    if [[ "$inc402_status" != "RESOLVED" ]]; then
      cur_tmpl="$(kubectl ate get actor inc-402 -a "$ATESPACE" -o json 2>/dev/null | jq -r '.actorTemplate.name // empty' || true)"
      if [[ -n "$cur_tmpl" && "$cur_tmpl" != "sre-agent-v4" ]]; then
        delete_actor_and_wait "$ATESPACE" inc-402
      fi

      gate_code="$(curl -sS -o "$STATE_DIR/catchup-mod4-gate.json" -w '%{http_code}' \
        -X POST "${gw_url}/api/chat" \
        -H "Content-Type: application/json" \
        -d '{"incident_id":"INC-402","template":"sre-agent-v4","prompt":"Stage and remediate INC-402 end-to-end: query MCP logs and health, patch config/pool.yaml and pool/store.go on branch fix/inc-402-pool-leak, verify with ledger-verify, push the branch, and apply_remediation_pr."}')" || gate_code="000"
      if [[ "$gate_code" != "200" ]]; then
        sleep 8
        gate_code="$(curl -sS -o "$STATE_DIR/catchup-mod4-gate.json" -w '%{http_code}' \
          -X POST "${gw_url}/api/chat" \
          -H "Content-Type: application/json" \
          -d '{"incident_id":"INC-402","template":"sre-agent-v4","prompt":"Stage and remediate INC-402 end-to-end: query MCP logs and health, patch config/pool.yaml and pool/store.go on branch fix/inc-402-pool-leak, verify with ledger-verify, push the branch, and apply_remediation_pr."}')" || gate_code="000"
      fi
      [[ "$gate_code" == "200" ]] || die "Module 04 gated INC-402 turn returned HTTP $gate_code"

      app_code="$(curl -sS -o "$STATE_DIR/catchup-mod4-approve.json" -w '%{http_code}' \
        -X POST "${gw_url}/api/incidents/INC-402/remediation/approve" \
        -H "Content-Type: application/json" \
        -d '{}')" || app_code="000"
      [[ "$app_code" == "200" ]] || die "Module 04 Remediation Gate approval returned HTTP $app_code"
    fi

    suspend_actor "$ATESPACE" inc-402
    stop_port_forward catchup-gw
    trap - EXIT
    ok "INC-402 remediated via Agent Registry MCP + Zero-Compute Remediation Gate on sre-agent-v4"
  fi
fi

# Downstream modules (N >= 6) attach their progressive stages here as Modules 05-06 land.
if (( N >= 6 )); then
  step "Module 05 end state: Model Armor, GCPAuthzPolicy, 3LO Auth Manager, and zero-token snapshot audit"
  unset DEFAULT_ACTOR_TEMPLATE GOVERNANCE_ENFORCED REQUIRE_3LO_FOR_REMEDIATION WORKSHOP_GOV_OVERRIDE WORKSHOP_3LO_OVERRIDE WORKSHOP_TEMPLATE_OVERRIDE
  WORKSHOP_TEMPLATE_OVERRIDE=sre-agent-v4 DEFAULT_ACTOR_TEMPLATE=sre-agent-v4 "$S/setup-governance.sh"

  ephemeral_port_forward catchup-gw "$NAMESPACE" svc/sre-gateway 18081 8080
  gw_url="$PF_URL"
  trap 'stop_port_forward catchup-gw' EXIT

  REV=v1 BROKER_SYNC_URL="${gw_url}" "$S/publish-skill.sh"
  REV=v2 BROKER_SYNC_URL="${gw_url}" "$S/publish-skill.sh"

  armor_code="$(curl -sS -o "$STATE_DIR/catchup-mod5-inc409.json" -w '%{http_code}' \
    -X POST "${gw_url}/api/chat" \
    -H "Content-Type: application/json" \
    -d '{"incident_id":"INC-409","template":"sre-agent-v4","prompt":"Review the attached partner diagnostic note on INC-409 and summarize next steps."}')" || armor_code="000"
  [[ "$armor_code" == "403" ]] || die "expected INC-409 prompt-injection block (HTTP 403), got HTTP $armor_code"

  mcp_deny_code="$(curl -sS -o /dev/null -w '%{http_code}' \
    -X POST "${gw_url}/mcp/ops-hub-mcp" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"drop_ledger_database","arguments":{"confirm":true}}}')" || mcp_deny_code="000"
  [[ "$mcp_deny_code" == "403" ]] || die "expected destructive MCP tool denial (HTTP 403), got HTTP $mcp_deny_code"

  complete_3lo_pkce_grant "${gw_url}" || die "failed to complete 3LO OAuth + S256 PKCE grant against sre-gateway"

  pulls_json="$(curl -fsS "${gw_url}/ops/api/pulls" 2>/dev/null || echo '[]')"
  has_dual_pr="$(jq -r '[.[]? | select(.agentic_delegation == true and (.operator_sub | length > 0) and (.agent_identity | startswith("spiffe://")))] | length' <<<"$pulls_json" 2>/dev/null || echo 0)"
  sessions_json="$(curl -fsS "${gw_url}/api/ledger/sessions" 2>/dev/null || echo '{}')"
  has_inc402_sess="$(jq -r '[.sessions[]? | select(.incident_id == "INC-402" and .event_count >= 2)] | length' <<<"$sessions_json" 2>/dev/null || echo 0)"
  if (( has_dual_pr == 0 || has_inc402_sess == 0 )); then
    cur_tmpl="$(kubectl ate get actor inc-402 -a "$ATESPACE" -o json 2>/dev/null | jq -r '.actorTemplate.name // empty' || true)"
    if [[ -n "$cur_tmpl" && "$cur_tmpl" != "sre-agent-v4" ]]; then
      delete_actor_and_wait "$ATESPACE" inc-402
    fi
    gate5_code="$(curl -sS -o "$STATE_DIR/catchup-mod5-gate.json" -w '%{http_code}' \
      -X POST "${gw_url}/api/chat" \
      -H "Content-Type: application/json" \
      -d '{"incident_id":"INC-402","template":"sre-agent-v4","prompt":"Stage and remediate INC-402 end-to-end using ledger-pool-triage v2: query MCP logs and health, patch config/pool.yaml and pool/store.go on branch fix/inc-402-pool-leak, verify with ledger-verify, push the branch, and apply_remediation_pr."}')" || gate5_code="000"
    if [[ "$gate5_code" != "200" ]]; then
      sleep 8
      gate5_code="$(curl -sS -o "$STATE_DIR/catchup-mod5-gate.json" -w '%{http_code}' \
        -X POST "${gw_url}/api/chat" \
        -H "Content-Type: application/json" \
        -d '{"incident_id":"INC-402","template":"sre-agent-v4","prompt":"Stage and remediate INC-402 end-to-end using ledger-pool-triage v2: query MCP logs and health, patch config/pool.yaml and pool/store.go on branch fix/inc-402-pool-leak, verify with ledger-verify, push the branch, and apply_remediation_pr."}')" || gate5_code="000"
    fi
    [[ "$gate5_code" == "200" ]] || die "Module 05 gated INC-402 turn returned HTTP $gate5_code"

    if jq -e '.remediation_gate_pending == true' "$STATE_DIR/catchup-mod5-gate.json" >/dev/null 2>&1; then
      app5_code="$(curl -sS -o "$STATE_DIR/catchup-mod5-approve.json" -w '%{http_code}' \
        -X POST "${gw_url}/api/incidents/INC-402/remediation/approve" \
        -H "Content-Type: application/json" \
        -d '{}')" || app5_code="000"
      [[ "$app5_code" == "200" ]] || die "Module 05 3LO Remediation Gate approval returned HTTP $app5_code"
    fi
  fi

  has_inc403_sess="$(jq -r '[.sessions[]? | select(.incident_id == "INC-403" and .event_count >= 2)] | length' <<<"$sessions_json" 2>/dev/null || echo 0)"
  if (( has_inc403_sess == 0 )); then
    sib5_code="$(curl -sS -o "$STATE_DIR/catchup-mod5-403.json" -w '%{http_code}' \
      -X POST "${gw_url}/api/chat" \
      -H "Content-Type: application/json" \
      -d '{"incident_id":"INC-403","template":"sre-agent-v4","prompt":"Triage sibling incident INC-403 using Consolidated Memory from prior ledger-api incidents."}')" || sib5_code="000"
    [[ "$sib5_code" == "200" ]] || die "Module 05 sibling INC-403 recall turn returned HTTP $sib5_code"
    suspend_actor "$ATESPACE" inc-403 >/dev/null 2>&1 || true
  fi

  suspend_actor "$ATESPACE" inc-402
  if (( N == 6 )); then
    AUDIT_GATEWAY_URL="${gw_url}" "$S/audit-snapshot.sh"
  fi

  stop_port_forward catchup-gw
  trap - EXIT
  ok "Module 05 zero-trust governance, 3LO dual attribution, and zero-token snapshot audit verified"
fi

if (( N >= 7 )); then
  step "Module 06 end state: GMP PodMonitoring, W3C Cloud Trace waterfall, and pure-Go online evals"
  "$S/observability.sh"
  mkdir -p "$REPO_ROOT/bin"
  (cd "$REPO_ROOT/app" && go build -o ../bin/evalrunner ./cmd/evalrunner)

  ephemeral_port_forward catchup-gw "$NAMESPACE" svc/sre-gateway 18081 8080
  gw_url="$PF_URL"
  trap 'stop_port_forward catchup-gw' EXIT

  tp_code="$(curl -sS -o "$STATE_DIR/catchup-mod6-trace.json" -w '%{http_code}' \
    -X POST "${gw_url}/api/chat" \
    -H "Content-Type: application/json" \
    -H "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" \
    -d '{"incident_id":"INC-403","template":"sre-agent-v4","prompt":"Triage sibling incident INC-403 using Consolidated Memory from prior ledger-api incidents."}')" || tp_code="000"
  [[ "$tp_code" == "200" ]] || die "Module 06 W3C traceparent turn returned HTTP $tp_code"

  "$REPO_ROOT/bin/evalrunner" --gateway-url "${gw_url}" --min-score 0.80 --output "$STATE_DIR/eval-report.json"
  suspend_actor "$ATESPACE" inc-403 >/dev/null 2>&1 || true

  stop_port_forward catchup-gw
  trap - EXIT
  ok "Module 06 OpenTelemetry GenAI waterfall and 4-rubric pure-Go online evals verified"
fi
moddir=""
for d in "$REPO_ROOT/modules/0${N}-"*/; do
  [[ -d "$d" ]] || continue
  moddir="${d%/}"
  moddir="${moddir##*/}"
  break
done
if (( N == 0 )); then
  ok "module 00 setup complete; verify with: make verify MODULE=0"
elif [[ -n "$moddir" ]]; then
  ok "ready for module $N; open modules/$moddir/README.md"
else
  ok "catch-up complete; ready for module $N"
fi
user_has_ate || path_hint
