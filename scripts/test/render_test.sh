#!/usr/bin/env bash
# render.sh fills ${VARS} from workshop.env and .workshop/ state, and names the make target when state is missing.
# shellcheck disable=SC2034 # $out is read inside check() eval strings
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
export WORKSHOP_ENV_FILE="$TMP/env" WORKSHOP_STATE_DIR="$TMP/ws"
printf 'PROJECT_ID=p\nBUCKET_NAME=b\n' >"$WORKSHOP_ENV_FILE"
mkdir -p "$WORKSHOP_STATE_DIR"
fails=0
check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; fails=$((fails+1)); fi; }

printf 'image: ${AGENT_IMAGE}\nbucket: gs://${BUCKET_NAME}/${ATESPACE}/\nkeep: $HOME\n' >"$TMP/t.yaml.tmpl"
echo "r/agent@sha256:abc" >"$WORKSHOP_STATE_DIR/agent-image"
out="$(bash "$ROOT/scripts/render.sh" "$TMP/t.yaml.tmpl" 2>&1)"
check "fills state-backed vars" "[[ \"\$out\" == *'image: r/agent@sha256:abc'* ]]"
check "fills env vars and defaults" "[[ \"\$out\" == *'bucket: gs://b/workshop/'* ]]"
check "leaves non-template text alone" "[[ \"\$out\" == *'keep: \$HOME'* ]]"

rm "$WORKSHOP_STATE_DIR/agent-image"
out="$(bash "$ROOT/scripts/render.sh" "$TMP/t.yaml.tmpl" 2>&1)"; rc=$?
check "missing state fails" "[[ $rc -ne 0 ]]"
check "missing state names the make target" "[[ \"\$out\" == *'make build-agent'* ]]"
exit "$fails"
