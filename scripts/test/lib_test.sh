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

# Unit tests for scripts/lib.sh. No cluster needed. Run: make test-scripts
# shellcheck disable=SC2329,SC2030,SC2031 # t_* run via check(); per-test PATH/KLOG exports are subshell-local on purpose
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
t_user_has_ate() {
  # Checks the caller's PATH (captured before lib.sh prepends bin/), not the script's own.
  ! (WORKSHOP_USER_PATH="/usr/bin:/bin" user_has_ate) &&
    (WORKSHOP_USER_PATH="/usr/bin:$REPO_ROOT/bin/" user_has_ate) &&
    [[ "$(WORKSHOP_USER_PATH=/usr/bin path_hint 2>&1)" == *"export PATH=\"$REPO_ROOT/bin:"* ]]
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
t_load_env_mock_llm_override() {
  local f="$TMP/mock.env"; printf 'PROJECT_ID=p\nMOCK_LLM=false\n' >"$f"
  # From workshop.env when nothing is passed; the command-line value wins, also in a nested script.
  [[ "$(unset MOCK_LLM WORKSHOP_MOCK_LLM_OVERRIDE; WORKSHOP_ENV_FILE="$f" load_env; echo "$MOCK_LLM")" == false ]] &&
  [[ "$(unset WORKSHOP_MOCK_LLM_OVERRIDE; export MOCK_LLM=true WORKSHOP_ENV_FILE="$f"; load_env
       bash -c 'source "$1"; load_env; echo "$MOCK_LLM"' _ "$HERE/../lib.sh")" == true ]] &&
  # A parent script without an override hands its children an empty (not a workshop.env) override.
  [[ "$(unset MOCK_LLM WORKSHOP_MOCK_LLM_OVERRIDE; WORKSHOP_ENV_FILE="$f" load_env
       bash -c 'echo "[${WORKSHOP_MOCK_LLM_OVERRIDE-unset}]"')" == "[]" ]]
}
t_load_env_cluster_vars() {
  local n="$TMP/new.env" o="$TMP/old.env"
  printf 'PROJECT_ID=p\nCLUSTER_NAME=c1\nCLUSTER_LOCATION=us-east1-b\n' >"$n"
  printf 'PROJECT_ID=p\nCLUSTER=c2\nZONE_OR_REGION=europe-west1\n' >"$o"
  # ate-setup is a child process: the names it reads must be exported, and legacy names still work.
  [[ "$(unset CLUSTER_NAME CLUSTER_LOCATION; WORKSHOP_ENV_FILE="$n" load_env
       bash -c 'echo "$CLUSTER_NAME/$CLUSTER_LOCATION"')" == "c1/us-east1-b" ]] &&
  [[ "$(unset CLUSTER_NAME CLUSTER_LOCATION; WORKSHOP_ENV_FILE="$o" load_env
       bash -c 'echo "$CLUSTER_NAME/$CLUSTER_LOCATION"')" == "c2/europe-west1" ]]
}
# gateway.sh against a stub kubectl whose live Deployment has MOCK_LLM=$LIVE_MOCK; prints the applied MOCK_LLM.
run_gateway_sh() {
  local d; d="$(mktemp -d "$TMP/t.XXXXXX")"; mkdir -p "$d/bin" "$d/state"
  cat >"$d/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  *"get deploy gateway"*"IDLE_AFTER"*) echo 20s ;;
  *"get deploy gateway"*"MOCK_LLM"*) echo "$LIVE_MOCK" ;;
  *"create namespace"*) echo "kind: Namespace" ;;
  *"apply -f -"*) cat >>"$APPLIED" ;;
esac
exit 0
STUB
  chmod +x "$d/bin/kubectl"
  echo "r/gw@sha256:abc" >"$d/state/gateway-image"
  printf 'PROJECT_ID=p\nMOCK_LLM=false\n' >"$d/env"
  env -u MOCK_LLM -u WORKSHOP_MOCK_LLM_OVERRIDE PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" \
    WORKSHOP_STATE_DIR="$d/state" APPLIED="$d/applied" "$@" bash "$HERE/../gateway.sh" >/dev/null 2>&1 || return 1
  grep -A1 'name: MOCK_LLM' "$d/applied" | sed -n 's/.*value: "\([a-z]*\)".*/\1/p' | head -n1
}
t_gateway_keeps_live_mock_llm() {
  [[ "$(run_gateway_sh LIVE_MOCK=true)" == true ]] &&     # live beats workshop.env
    [[ "$(run_gateway_sh LIVE_MOCK=)" == false ]] &&      # no gateway yet: workshop.env
    [[ "$(run_gateway_sh LIVE_MOCK=true MOCK_LLM=false)" == false ]]   # command line beats live
}
t_stop_port_forward_stops_one() {
  sleep 30 & local a=$!; sleep 30 & local b=$!
  echo "$a" >"$STATE_DIR/pf-one.pid"; echo "$b" >"$STATE_DIR/pf-two.pid"
  stop_port_forward one; stop_port_forward missing
  local ok=true
  kill -0 "$a" 2>/dev/null && ok=false
  [[ -f "$STATE_DIR/pf-one.pid" ]] && ok=false
  kill -0 "$b" 2>/dev/null || ok=false
  stop_port_forwards
  kill -0 "$b" 2>/dev/null && ok=false
  wait "$a" "$b" 2>/dev/null
  $ok
}
t_gateway_chat_prints_body_then_status() {
  local d; d="$(mktemp -d "$TMP/t.XXXXXX")"; mkdir -p "$d/bin"
  # A tiny HTTP server standing in for the gateway: any POST gets a fixed JSON reply.
  cat >"$d/srv.py" <<'PY'
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length', '0')))
        body = b'{"reply":"stub reply","turn":1,"backend":"mock"}'
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
PY
  # A stub kubectl: `port-forward RES LPORT:RPORT` starts the fake server on LPORT instead
  # of actually forwarding, so gateway_chat's curl reaches something real.
  cat >"$d/bin/kubectl" <<EOS
#!/usr/bin/env bash
lport=""
for a in "\$@"; do case "\$a" in *:*) lport="\${a%%:*}" ;; esac; done
exec python3 "$d/srv.py" "\$lport"
EOS
  chmod +x "$d/bin/kubectl"
  local out rc
  # A free port, so a real gateway forward on 8081 (e.g. a live workshop) doesn't break the test.
  local port; port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
  out="$(PATH="$d/bin:$PATH" NAMESPACE=ws GATEWAY_LOCAL_PORT="$port" gateway_chat buddy "hi" 2>&1)"; rc=$?
  kill "$(cat "$STATE_DIR/pf-gateway.pid" 2>/dev/null)" 2>/dev/null || true
  rm -f "$STATE_DIR/pf-gateway.pid"
  [[ $rc -eq 0 && "${out##*$'\n'}" == "200" && "${out%$'\n'*}" == *'"reply":"stub reply"'* ]]
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

# A stub kubectl that fakes `kubectl ate` for one actor a1 in atespace ws.
# KSTATE holds the actor's state; an absent file means the actor doesn't exist.
make_stub_kubectl() {
  local d="$1"; mkdir -p "$d/bin"
  cat >"$d/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$KLOG"
case "$*" in
  "ate get actor a1 -a ws -o json")
    [[ -f "$KSTATE" ]] || exit 1
    printf '{"metadata":{"name":"a1"},"status":{"state":"%s"}}\n' "$(cat "$KSTATE")" ;;
  "ate get actors -a ws -o json")
    printf '{"actors":[{"metadata":{"name":"a1"},"status":{"state":"%s"}},{"metadata":{"name":"b1"},"status":{"state":"ACTOR_STATE_RUNNING"}}]}\n' "$(cat "$KSTATE")" ;;
  "ate create actor a1 --template=t -a ws") echo SUSPENDED >"$KSTATE" ;;
  "ate suspend actor a1 -a ws") echo SUSPENDED >"$KSTATE" ;;
  *) exit 1 ;;
esac
STUB
  chmod +x "$d/bin/kubectl"
}
t_ensure_actor_idempotent() {
  local d; d="$(mktemp -d "$TMP/t.XXXXXX")"; make_stub_kubectl "$d"
  ( export PATH="$d/bin:$PATH" KLOG="$d/log" KSTATE="$d/state"
    ensure_actor ws a1 t && ensure_actor ws a1 t )
  [[ "$(grep -c 'create actor' "$d/log")" == 1 ]]
}
t_suspend_actor_suspends_running() {
  local d; d="$(mktemp -d "$TMP/t.XXXXXX")"; make_stub_kubectl "$d"; echo RUNNING >"$d/state"
  ( export PATH="$d/bin:$PATH" KLOG="$d/log" KSTATE="$d/state"; suspend_actor ws a1 ) &&
    [[ "$(cat "$d/state")" == SUSPENDED ]] && grep -q 'suspend actor a1' "$d/log"
}
t_suspend_actor_noop_when_suspended() {
  local d; d="$(mktemp -d "$TMP/t.XXXXXX")"; make_stub_kubectl "$d"; echo SUSPENDED >"$d/state"
  ( export PATH="$d/bin:$PATH" KLOG="$d/log" KSTATE="$d/state"; suspend_actor ws a1 ) &&
    ! grep -q 'suspend actor' "$d/log"
}
t_count_actors_in_state() {
  local d; d="$(mktemp -d "$TMP/t.XXXXXX")"; make_stub_kubectl "$d"; echo SUSPENDED >"$d/state"
  local n; n="$(export PATH="$d/bin:$PATH" KLOG="$d/log" KSTATE="$d/state"; count_actors_in_state ws RUNNING)"
  local m; m="$(export PATH="$d/bin:$PATH" KLOG="$d/log" KSTATE="$d/state"; count_actors_in_state ws SUSPENDED a)"
  [[ "$n" == 1 && "$m" == 1 ]]
}

# A stub kubectl for reset_template: $KDIR/actors/<name> holds the actor's template name,
# $KDIR/tmpl-<name> marks a template that exists. Deleting a template still in use fails,
# like ateapi's FailedPrecondition.
make_template_stub() {
  local d="$1"; mkdir -p "$d/bin" "$d/actors"
  cat >"$d/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$KLOG"
case "$*" in
  "ate get actors -a ws -o json")
    out=""; for f in "$KDIR"/actors/*; do [[ -f "$f" ]] || continue
      out+="${out:+,}{\"metadata\":{\"name\":\"${f##*/}\"},\"actorTemplate\":{\"atespace\":\"ws\",\"name\":\"$(cat "$f")\"},\"status\":{\"state\":\"ACTOR_STATE_CRASHED\"}}"
    done; echo "{\"actors\":[$out]}" ;;
  "ate delete actor "*" -a ws --any-state") rm -f "$KDIR/actors/$4" ;;
  "ate get actor-template "*" -a ws") [[ -f "$KDIR/tmpl-$4" ]] ;;
  "ate delete actor-template "*" -a ws")
    grep -qx "$4" "$KDIR"/actors/* 2>/dev/null && exit 1
    rm -f "$KDIR/tmpl-$4" ;;
  *) exit 1 ;;
esac
STUB
  chmod +x "$d/bin/kubectl"
}
t_reset_template_deletes_its_actors_then_template() {
  local d; d="$(mktemp -d "$TMP/t.XXXXXX")"; make_template_stub "$d"
  echo t >"$d/actors/a1"; echo t >"$d/actors/a2"; echo other >"$d/actors/b1"; touch "$d/tmpl-t" "$d/tmpl-other"
  ( export PATH="$d/bin:$PATH" KLOG="$d/log" KDIR="$d"; reset_template ws t && reset_template ws t ) &&
    [[ ! -e "$d/actors/a1" && ! -e "$d/actors/a2" && -e "$d/actors/b1" ]] &&
    [[ ! -e "$d/tmpl-t" && -e "$d/tmpl-other" ]] &&
    grep -q 'delete actor a1 -a ws --any-state' "$d/log" && ! grep -q 'delete actor b1' "$d/log"
}
t_catch_up_reset_needs_module_3() {
  local out; out="$(RESET=1 bash "$HERE/../catch-up.sh" 2 2>&1)" && return 1
  [[ "$out" == *"RESET=1 needs MODULE=3"* ]]
}

load_env
check "load_env expands references" t_load_env_expands
check "load_env requires PROJECT_ID" t_load_env_requires_project
check "user_has_ate checks the caller's PATH" t_user_has_ate
check "state roundtrip" t_state_roundtrip
check "require_state names the make target" t_require_state_names_target
check "render substitutes only listed vars" t_render_only_listed_vars
check "render fails on unset var" t_render_fails_on_unset
check "wait_for succeeds" t_wait_for_succeeds
check "wait_for times out" t_wait_for_times_out
check "port_forward detects foreign listener" t_port_forward_detects_foreign_listener
check "port_forward reuses live forward" t_port_forward_reuses_live
check "load_env: command-line MOCK_LLM beats workshop.env" t_load_env_mock_llm_override
check "load_env exports CLUSTER_NAME/CLUSTER_LOCATION (legacy names too)" t_load_env_cluster_vars
check "gateway.sh keeps a live MOCK_LLM unless one is passed" t_gateway_keeps_live_mock_llm
check "stop_port_forward stops only the named forward" t_stop_port_forward_stops_one
check "gateway_chat prints body then status code" t_gateway_chat_prints_body_then_status
check "actor_state strips ACTOR_STATE_ prefix" t_actor_state_strips_prefix
check "ensure_actor creates once" t_ensure_actor_idempotent
check "suspend_actor suspends a running actor" t_suspend_actor_suspends_running
check "suspend_actor is a no-op when suspended" t_suspend_actor_noop_when_suspended
check "count_actors_in_state filters by state and prefix" t_count_actors_in_state
check "catch-up RESET=1 refuses MODULE<3" t_catch_up_reset_needs_module_3
check "reset_template deletes its actors, then the template (idempotent)" t_reset_template_deletes_its_actors_then_template
exit "$fails"
