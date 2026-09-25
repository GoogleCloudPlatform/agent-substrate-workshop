#!/usr/bin/env bash
# Lints observability/dashboard.json and proves dashboards.sh is idempotent (create once, then update
# with the live etag) using a stub gcloud. No cloud access needed.
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
exit "$fails"
