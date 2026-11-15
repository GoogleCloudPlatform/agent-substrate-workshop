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

# Module 02 end state: sre-agent-v2 warmed with a Golden Snapshot, zero-wake Session Ledger
# inspection while inc-402 remains SUSPENDED (0 vCPU), and cross-rollout / sibling-actor (inc-403)
# Consolidated Memory recall via sre-gateway.
source "$(dirname "$0")/common.sh"

step "verify module 02 (zero-wake session ledger & consolidated memory)"
check "sre-gateway deployment available in $NAMESPACE" \
  kubectl -n "$NAMESPACE" wait --for=condition=Available deploy/sre-gateway --timeout=30s
check "ActorTemplate sre-agent-v2 has a warmed Golden Snapshot" \
  _template_ready "$ATESPACE" sre-agent-v2

ephemeral_port_forward verify-gw "$NAMESPACE" svc/sre-gateway 18081 8080
GW_URL="$PF_URL"
trap 'stop_port_forward verify-gw' EXIT

check "Incident Console /sessions view serves Session ledger & Consolidated memory panels" \
  bash -c '
    html="$(curl -fsS "$1/sessions")" &&
      grep -q "Session ledger" <<<"$html" &&
      grep -q "Consolidated memory" <<<"$html" &&
      grep -q "memory-search-input" <<<"$html"
  ' _ "$GW_URL"

inc402_body="$(curl -fsS -X POST "$GW_URL/api/chat" \
  -H "Content-Type: application/json" \
  -d '{"incident_id":"INC-402","template":"sre-agent-v2","prompt":"Investigate INC-402 by cloning ledger-api into /workspace, reading config/pool.yaml and pool/store.go, and running ledger-verify."}' 2>/dev/null || true)"

check "POST /api/chat runs INC-402 on sre-agent-v2 and persists to Session Ledger" \
  jq -e '.actor_name == "inc-402" and .template == "sre-agent-v2" and (.starter_stub // false) == false' <<<"$inc402_body"

suspend_actor "$ATESPACE" inc-402

ledger_detail="$(curl -fsS "$GW_URL/api/ledger/sessions/inc-402" 2>/dev/null || true)"
mem_detail="$(curl -fsS "${GW_URL}/api/ledger/memories?q=MAX_OPEN_CONNS" 2>/dev/null || true)"

check "GET /api/ledger/sessions/inc-402 returns structured turn events while inc-402 is SUSPENDED" \
  jq -e '.session_id == "inc-402" and .actor_state == "SUSPENDED" and (.events | length >= 3)' <<<"$ledger_detail"
check "Consolidated Memory extracted INC-402 pool-sizing and cursor-leak facts" \
  jq -e '(.memories | length >= 1) and ([.memories[].fact] | join(" ") | test("MAX_OPEN_CONNS|max_open_conns"; "i"))' <<<"$mem_detail"
check "zero-wake Session Ledger & Memory queries kept inc-402 SUSPENDED (0 vCPU)" \
  _actor_in_state "$ATESPACE" inc-402 SUSPENDED

inc403_body="$(curl -fsS -X POST "$GW_URL/api/chat" \
  -H "Content-Type: application/json" \
  -d '{"incident_id":"INC-403","template":"sre-agent-v2","prompt":"Triage sibling incident INC-403 using Consolidated Memory from prior ledger-api incidents."}' 2>/dev/null || true)"

check "sibling Incident Actor inc-403 recalls consolidated INC-402 root-cause facts" \
  jq -e '.actor_name == "inc-403" and .template == "sre-agent-v2" and (.reply | test("INC-402")) and (.reply | test("MAX_OPEN_CONNS|max_open_conns"; "i"))' <<<"$inc403_body"

stop_port_forward verify-gw
trap - EXIT
finish 2
