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

# Lints observability/dashboard.json, proves dashboards.sh is idempotent (create once, then update
# with the live etag), and checks what teardown.sh deletes, all with stub gcloud/kubectl. No cloud access needed.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DASH="$ROOT/observability/dashboard.json"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
fails=0
check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; fails=$((fails+1)); fi; }

check "dashboard is valid JSON" "jq -e . '$DASH' >/dev/null"
check "displayName is 'Agent Substrate Workshop'" "[[ \$(jq -r .displayName '$DASH') == 'Agent Substrate Workshop' ]]"
check "tiles fit a 12-column grid" "jq -e '[.mosaicLayout.tiles[] | (.xPos // 0) + .width <= 12] | all' '$DASH' >/dev/null"
check "every tile has a widget title" "jq -e '[.mosaicLayout.tiles[] | .widget.title | length > 0] | all' '$DASH' >/dev/null"
# Dotted OTel metric and label names must be quoted in PromQL: strip quoted strings, then look for a.b.
# shellcheck disable=SC2034 # unquoted is read inside the eval'd check() string below
unquoted="$(jq -r '.. | .prometheusQuery? // empty' "$DASH" | sed -E 's/"[^"]*"//g' | grep -E '[a-z_]\.[a-z]' || true)"
check "dotted names are quoted in every query" "[[ -z \"\$unquoted\" ]]"
check "has at least 10 PromQL queries" "(( \$(jq '[.. | .prometheusQuery? // empty] | length' '$DASH') >= 10 ))"

# Idempotency: stub gcloud keeps one dashboard in a state file.
mkdir -p "$TMP/bin"
cat >"$TMP/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$STUB_LOG"
case "$*" in
  *"dashboards list"*) [[ -f "$STUB_STATE" ]] && cat "$STUB_STATE"; exit 0 ;;
  *"dashboards create"*) echo -e "projects/p/dashboards/123\tetag-1" >"$STUB_STATE" ;;
  *"dashboards update"*) cat >"$STUB_UPDATE_BODY" ;;
esac
STUB
chmod +x "$TMP/bin/gcloud"
export STUB_LOG="$TMP/log" STUB_STATE="$TMP/state" STUB_UPDATE_BODY="$TMP/body" PATH="$TMP/bin:$PATH"
export WORKSHOP_ENV_FILE="$TMP/env" WORKSHOP_STATE_DIR="$TMP/ws"
echo "PROJECT_ID=p" >"$WORKSHOP_ENV_FILE"
( source "$ROOT/scripts/dashboards.sh" --source-only; load_env; upsert_dashboard "$DASH" ) >/dev/null
( source "$ROOT/scripts/dashboards.sh" --source-only; load_env; upsert_dashboard "$DASH" ) >/dev/null
check "first run creates" "[[ \$(grep -c 'dashboards create' '$TMP/log') == 1 ]]"
check "second run updates the same dashboard" "grep -q 'dashboards update projects/p/dashboards/123' '$TMP/log'"
check "update carries the live etag" "[[ \$(jq -r .etag '$TMP/body') == etag-1 ]]"
check "uploaded queries are pinned to the cluster" "jq -e '[.. | .prometheusQuery? // empty | contains(\"cluster=\\\"ate-cluster\\\"\")] | all and length >= 10' '$TMP/body' >/dev/null"

check "uploaded logs panels are pinned to the cluster" "jq -e '[.. | .logsPanel? // empty | .filter | contains(\"resource.labels.cluster_name=\\\"ate-cluster\\\"\")] | all and length >= 1' '$TMP/body' >/dev/null"

# promql_scope.py: every selector form the dashboards use, and the things that must stay untouched.
scope() { python3 "$ROOT/scripts/promql_scope.py" query c "$1"; }
scope_is() { [[ "$(scope "$1")" == "$2" ]] || { echo "  got: $(scope "$1")"; return 1; }; }
check "scope: quoted selector" "scope_is '{\"a.b\", \"l\"=\"v\"}' '{\"a.b\", \"l\"=\"v\", cluster=\"c\"}'"
check "scope: bare name, range, by-list, number" "scope_is 'sum by (le, x) (rate(m_total[5m])) * 60' 'sum by (le, x) (rate(m_total{cluster=\"c\"}[5m])) * 60'"
check "scope: classic selector and or vector(0)" "scope_is 'm{a=\"b\"} or vector(0)' 'm{a=\"b\", cluster=\"c\"} or vector(0)'"
check "scope: existing cluster matcher kept" "scope_is 'm{cluster=\"z\"}' 'm{cluster=\"z\"}'"
check "scope: offset and subquery" "scope_is 'rate(m[5m:1m] offset 1h)' 'rate(m{cluster=\"c\"}[5m:1m] offset 1h)'"

# Teardown deletes all four dashboards by title, and a failed delete doesn't stop it.
mkdir -p "$TMP/td/bin"
cat >"$TMP/td/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$STUB_LOG"
case "$*" in
  *"dashboards list"*) echo "projects/p/dashboards/$(printf '%s' "$*" | cksum | cut -d' ' -f1)" ;;
  *"dashboards delete"*) [[ "$*" != *"$FAIL_ID"* ]] ;;
  *"projects describe"*) echo 123 ;;
esac
STUB
cat >"$TMP/td/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
[[ "$1" == ate ]] && exit 1; exit 0
STUB
chmod +x "$TMP/td/bin/gcloud" "$TMP/td/bin/kubectl"
fail_id="$(printf '%s' "monitoring dashboards list --project p --filter=displayName=\"Substrate Snapshot Size & QPS\" --format=value(name)" | cksum | cut -d' ' -f1)"
: >"$TMP/td/log"
printf 'PROJECT_ID=p\nBUCKET_NAME=p-ate\nREGION=r\nAR_REPO=workshop\n' >"$TMP/td/env"
PATH="$TMP/td/bin:$PATH" STUB_LOG="$TMP/td/log" FAIL_ID="$fail_id" YES=1 WORKSHOP_ENV_FILE="$TMP/td/env" \
  bash "$ROOT/scripts/teardown.sh" >/dev/null 2>&1
td_rc=$?
check "teardown survives a failed dashboard delete" "[[ $td_rc == 0 ]] && grep -q 'dashboards delete projects/p/dashboards/$fail_id' '$TMP/td/log'"
for t in "Agent Substrate Workshop" "Substrate Routing & E2E Latency" "Substrate gRPC Server — latency / QPS / errors" "Substrate Snapshot Size & QPS"; do
  check "teardown looks up dashboard: $t" "grep -qF 'displayName=\"$t\"' '$TMP/td/log'"
done
check "teardown deletes each dashboard it finds" "[[ \$(grep -c 'dashboards delete' '$TMP/td/log') == 4 ]]"

# teardown --all must not touch project-level Artifact Registry grants (they're repo-level; the repo delete drops them).
: >"$TMP/td/log"
PATH="$TMP/td/bin:$PATH" STUB_LOG="$TMP/td/log" FAIL_ID=none YES=1 WORKSHOP_ENV_FILE="$TMP/td/env" \
  bash "$ROOT/scripts/teardown.sh" --all >/dev/null 2>&1
check "teardown --all deletes the repo and unbinds the workshop's project roles" \
  "grep -q 'artifacts repositories delete' '$TMP/td/log' && grep -q 'remove-iam-policy-binding.*aiplatform.user' '$TMP/td/log'"
check "teardown --all leaves project-level artifactregistry.reader alone" "! grep -q 'artifactregistry' '$TMP/td/log'"
exit "$fails"
