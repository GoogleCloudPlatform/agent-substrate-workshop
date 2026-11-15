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

# Module 03 end state: sre-agent-v3 warmed once with a Golden Snapshot, /catalog serving
# SHA-256 Procedural Skill revisions, and live v1 -> v2 runtime rollout on inc-402 with
# zero container rebuild and zero Golden Snapshot churn.
# shellcheck source=scripts/verify/common.sh
source "$(dirname "$0")/common.sh"

_catalog_ui_ready() {
  local html
  html="$(curl -fsS "$1/catalog")" &&
    grep -q "Procedural skills" <<<"$html" &&
    grep -q "skills-registry-body" <<<"$html" &&
    grep -q "skill-revision-drawer" <<<"$html"
}

step "verify module 03 (dynamic procedural skills via Skill Registry)"
check "sre-gateway deployment available in $NAMESPACE" \
  kubectl -n "$NAMESPACE" wait --for=condition=Available deploy/sre-gateway --timeout=30s
check "ActorTemplate sre-agent-v3 has a warmed Golden Snapshot" \
  _template_ready "$ATESPACE" sre-agent-v3

golden_tag_before="$(kubectl ate get actor-template sre-agent-v3 -a "$ATESPACE" -o json 2>/dev/null | jq -r '.status.goldenSnapshotStatus.goldenTag.name // empty' || true)"

ephemeral_port_forward verify-gw "$NAMESPACE" svc/sre-gateway 18081 8080
GW_URL="$PF_URL"
trap 'stop_port_forward verify-gw' EXIT

check "Incident Console /catalog view serves Skill Registry table and revision drawer" \
  _catalog_ui_ready "$GW_URL"

REV=v1 BROKER_SYNC_URL="$GW_URL" "$REPO_ROOT/scripts/publish-skill.sh" >/dev/null
cat_v1="$(curl -fsS "$GW_URL/api/catalog/skills" 2>/dev/null || true)"
check "Skill Registry exposes ledger-pool-triage@v1 with 64-char SHA-256 digest" \
  jq -e '.skills[0].name == "ledger-pool-triage" and .skills[0].default_revision == "v1" and (.skills[0].active_sha256 | length == 64)' <<<"$cat_v1"

turn_v1="$(curl -fsS -X POST "$GW_URL/api/chat" \
  -H "Content-Type: application/json" \
  -d '{"incident_id":"INC-402","template":"sre-agent-v3","prompt":"Run the ledger-pool-triage procedural skill against INC-402 and report the SKILL_REVISION findings."}' 2>/dev/null || true)"
ledger_v1="$(curl -fsS "$GW_URL/api/ledger/sessions/inc-402" 2>/dev/null || echo '{}')"
prior_load_skill="$(jq -r '[(.events // [])[]?.tool_calls[]?.name] | index("load_skill") != null' <<<"$ledger_v1" 2>/dev/null || echo false)"
[[ "$prior_load_skill" == "true" ]] || prior_load_skill="false"

# shellcheck disable=SC2016 # $prior_load_skill is a jq variable bound via --argjson
check "sre-agent-v3 executes load_skill + diagnose_pool.sh for SKILL_REVISION=v1" \
  jq -e --argjson prior_load_skill "$prior_load_skill" '
    .actor_name == "inc-402" and
    .template == "sre-agent-v3" and
    (
      (([.tool_steps[]?.name] | index("load_skill")) != null) or
      $prior_load_skill
    ) and
    (([.tool_steps[]?.name] | index("run_workspace_command")) != null) and
    (.reply | test("SKILL_REVISION=v1"))
  ' <<<"$turn_v1"

REV=v2 BROKER_SYNC_URL="$GW_URL" "$REPO_ROOT/scripts/publish-skill.sh" >/dev/null
cat_v2="$(curl -fsS "$GW_URL/api/catalog/skills" 2>/dev/null || true)"
check "Skill Registry promotes defaultRevision=v2 with distinct SHA-256 digest" \
  jq -e '.skills[0].default_revision == "v2" and (.skills[0].revisions | length >= 2)' <<<"$cat_v2"

turn_v2="$(curl -fsS -X POST "$GW_URL/api/chat" \
  -H "Content-Type: application/json" \
  -d '{"incident_id":"INC-402","template":"sre-agent-v3","prompt":"Re-run the ledger-pool-triage procedural skill on INC-402 and report the updated SKILL_REVISION findings."}' 2>/dev/null || true)"

check "resumed sre-agent-v3 dynamically pulls and executes SKILL_REVISION=v2 without image rebuild" \
  jq -e '.actor_name == "inc-402" and .template == "sre-agent-v3" and (.reply | test("SKILL_REVISION=v2")) and (.reply | test("CURSOR_LEAK_DETECTED=true|ReconcileTransfer"))' <<<"$turn_v2"

golden_tag_after="$(kubectl ate get actor-template sre-agent-v3 -a "$ATESPACE" -o json 2>/dev/null | jq -r '.status.goldenSnapshotStatus.goldenTag.name // empty' || true)"
check "sre-agent-v3 Golden Snapshot tag remained unchanged across v1 -> v2 skill rollout" \
  test -n "$golden_tag_before" -a "$golden_tag_before" = "$golden_tag_after"

stop_port_forward verify-gw
trap - EXIT
finish 3
