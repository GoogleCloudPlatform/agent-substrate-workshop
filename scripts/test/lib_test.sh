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
  out="$(PATH="$d/bin:$PATH" NAMESPACE=ws gateway_chat buddy "hi" 2>&1)"; rc=$?
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
check "gateway_chat prints body then status code" t_gateway_chat_prints_body_then_status
check "actor_state strips ACTOR_STATE_ prefix" t_actor_state_strips_prefix
check "ensure_actor creates once" t_ensure_actor_idempotent
check "suspend_actor suspends a running actor" t_suspend_actor_suspends_running
check "suspend_actor is a no-op when suspended" t_suspend_actor_noop_when_suspended
check "count_actors_in_state filters by state and prefix" t_count_actors_in_state
exit "$fails"
