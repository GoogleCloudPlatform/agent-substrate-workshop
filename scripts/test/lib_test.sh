#!/usr/bin/env bash
# Unit tests for scripts/lib.sh. No cluster needed. Run: make test-scripts
# shellcheck disable=SC2329 # t_* functions are invoked indirectly via check()
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
export WORKSHOP_ENV_FILE="$TMP/workshop.env" WORKSHOP_STATE_DIR="$TMP/state"
cat >"$WORKSHOP_ENV_FILE" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
BUCKET_NAME=${PROJECT_ID}-ate
ATESPACE=workshop
EOF
# shellcheck source=../lib.sh
source "$HERE/../lib.sh"

fails=0
check() { local name="$1"; shift; if "$@"; then echo "ok   $name"; else echo "FAIL $name"; fails=$((fails+1)); fi; }

t_load_env_expands() { load_env && [[ "$BUCKET_NAME" == "demo-proj-ate" ]]; }
t_load_env_requires_project() {
  local f="$TMP/bad.env"; echo "PROJECT_ID=" >"$f"
  ! (WORKSHOP_ENV_FILE="$f" load_env 2>/dev/null)
}
t_state_roundtrip() { state_put agent-image "r/agent@sha256:abc" && [[ "$(state_get agent-image)" == "r/agent@sha256:abc" ]]; }
t_require_state_names_target() {
  local out; out="$( (require_state gateway-image "make gateway") 2>&1 )"
  [[ "$out" == *"make gateway"* ]]
}
t_render_only_listed_vars() {
  printf 'a: ${ATESPACE}\nb: ${HOME_NOT_LISTED}\n' >"$TMP/t.tmpl"
  local out; out="$(render "$TMP/t.tmpl" ATESPACE)"
  [[ "$out" == $'a: workshop\nb: ${HOME_NOT_LISTED}' ]]
}
t_render_fails_on_unset() {
  printf 'x: ${NOPE_UNSET}\n' >"$TMP/u.tmpl"
  ! (render "$TMP/u.tmpl" NOPE_UNSET 2>/dev/null)
}
t_wait_for_succeeds() { wait_for 5 "trivial" true; }
t_wait_for_times_out() { ! (wait_for 1 "never" false 2>/dev/null); }
t_port_forward_detects_foreign_listener() {
  # A foreign process owns the port and we hold no pid file: must fail clearly.
  python3 -m http.server 18765 >/dev/null 2>&1 & local pid=$!
  sleep 0.5
  local out; out="$( (port_forward testpf default svc/none 18765 80) 2>&1 )"; local rc=$?
  kill "$pid"
  [[ $rc -ne 0 && "$out" == *"18765"*"in use"* ]]
}
t_port_forward_reuses_live() {
  python3 -m http.server 18766 >/dev/null 2>&1 & local pid=$!
  sleep 0.5
  echo "$pid" >"$STATE_DIR/pf-reuse.pid"
  port_forward reuse default svc/none 18766 80; local rc=$?
  kill "$pid"
  [[ $rc -eq 0 ]]
}
t_actor_state_strips_prefix() {
  local bindir="$TMP/bin"; mkdir -p "$bindir"
  cat >"$bindir/kubectl" <<'EOS'
#!/usr/bin/env bash
echo '{"status":{"state":"ACTOR_STATE_RUNNING"}}'
EOS
  chmod +x "$bindir/kubectl"
  local out; out="$(PATH="$bindir:$PATH" actor_state myatespace myactor)"
  [[ "$out" == "RUNNING" ]]
}

load_env
check "load_env expands references" t_load_env_expands
check "load_env requires PROJECT_ID" t_load_env_requires_project
check "state roundtrip" t_state_roundtrip
check "require_state names the make target" t_require_state_names_target
check "render substitutes only listed vars" t_render_only_listed_vars
check "render fails on unset var" t_render_fails_on_unset
check "wait_for succeeds" t_wait_for_succeeds
check "wait_for times out" t_wait_for_times_out
check "port_forward detects foreign listener" t_port_forward_detects_foreign_listener
check "port_forward reuses live forward" t_port_forward_reuses_live
check "actor_state strips ACTOR_STATE_ prefix" t_actor_state_strips_prefix
exit "$fails"
