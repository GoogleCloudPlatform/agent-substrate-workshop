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

# Module 01 end state: WorkerPool/sre-workers ready, sre-gateway serving the Incident Console
# and keyless /genai proxy, sre-agent-v1 warmed with a Golden Snapshot, inc-402 restricted by
# default-deny EgressPolicy to sre-gateway, Turn 1 diagnoses INC-402, and inc-402 suspends to 0 vCPU.
source "$(dirname "$0")/common.sh"

step "verify module 01 (actor boundary & first triage turn)"
check "2 sre-workers pods running in $NAMESPACE" \
  bash -c '[[ "$(kubectl -n "$1" get pods -l workload=sre-agent --field-selector=status.phase=Running -o name 2>/dev/null | wc -l)" -ge 2 ]]' _ "$NAMESPACE"
check "sre-gateway deployment available in $NAMESPACE" \
  kubectl -n "$NAMESPACE" wait --for=condition=Available deploy/sre-gateway --timeout=30s
check "ActorTemplate sre-agent-v1 has a warmed Golden Snapshot" \
  _template_ready "$ATESPACE" sre-agent-v1

ephemeral_port_forward verify-gw "$NAMESPACE" svc/sre-gateway 18081 8080
GW_URL="$PF_URL"
trap 'stop_port_forward verify-gw' EXIT

check "Incident Console serves / and /about on sre-gateway" \
  bash -c '
    curl -fsS "$1/" | grep -q "Gemini platform" &&
      curl -fsS "$1/about" | grep -q "Architecture" &&
      curl -fsS "$1/api/architecture" | jq -e ".nodes | length >= 8"
  ' _ "$GW_URL"

turn_body="$(curl -fsS -X POST "$GW_URL/api/chat" \
  -H "Content-Type: application/json" \
  -d '{"incident_id":"INC-402","prompt":"Investigate INC-402 by cloning ledger-api into /workspace, reading config/pool.yaml and pool/store.go, and running ledger-verify."}' 2>/dev/null || true)"

check "POST /api/chat completes Turn 1 on inc-402 with real ADK runner (starter_stub=false)" \
  jq -e '.actor_name == "inc-402" and (.starter_stub // false) == false and (.reply | length > 20)' <<<"$turn_body"
check "Incident Actor cloned ledger-api.git into /workspace" \
  jq -e '.workspace.cloned == true' <<<"$turn_body"
check "inc-402 carries an EgressPolicy allowing strictly sre-gateway.$NAMESPACE.svc.cluster.local" \
  bash -c 'kubectl ate get egress-policy inc-402 -a "$1" -o json | jq -e --arg h "sre-gateway.$2.svc.cluster.local" ".rules[0].http.hostnames == [\$h]"' _ "$ATESPACE" "$NAMESPACE"
check "idle-suspender transitions inc-402 to SUSPENDED (0 vCPU in Cloud Storage)" \
  wait_for 45 "inc-402 to auto-suspend" _actor_in_state "$ATESPACE" inc-402 SUSPENDED

stop_port_forward verify-gw
trap - EXIT
finish 1
