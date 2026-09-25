#!/usr/bin/env bash
# Shared helpers for workshop scripts. Source this file; do not execute it.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="${WORKSHOP_STATE_DIR:-$REPO_ROOT/.workshop}"
export PATH="$REPO_ROOT/bin:$PATH"

if [[ -t 1 ]]; then C_B=$'\e[1m'; C_G=$'\e[32m'; C_Y=$'\e[33m'; C_R=$'\e[31m'; C_0=$'\e[0m'; else C_B=; C_G=; C_Y=; C_R=; C_0=; fi
step() { echo "${C_B}==> $*${C_0}"; }
ok()   { echo "${C_G}  ✓ $*${C_0}"; }
warn() { echo "${C_Y}  ! $*${C_0}" >&2; }
die()  { echo "${C_R}  ✗ $*${C_0}" >&2; exit 1; }

# load_env reads workshop.env (auto-exporting every variable) and checks the essentials.
load_env() {
  local f="${WORKSHOP_ENV_FILE:-$REPO_ROOT/workshop.env}"
  [[ -f "$f" ]] || die "missing $f — run: cp workshop.env.example workshop.env and set PROJECT_ID"
  set -a; # shellcheck disable=SC1090
  source "$f"; set +a
  [[ -n "${PROJECT_ID:-}" ]] || die "PROJECT_ID is empty in $f"
  : "${NAMESPACE:=workshop}" "${ATESPACE:=workshop}" "${MOCK_LLM:=false}"
  export NAMESPACE ATESPACE MOCK_LLM
  mkdir -p "$STATE_DIR"
}

state_put() { mkdir -p "$STATE_DIR"; printf '%s\n' "$2" >"$STATE_DIR/$1"; }
state_get() { [[ -f "$STATE_DIR/$1" ]] && cat "$STATE_DIR/$1"; }
# require_state <file> <make target>: fail with the command that produces the file.
require_state() { [[ -s "$STATE_DIR/$1" ]] || die ".workshop/$1 is missing — run: $2"; }

# wait_for <timeout-seconds> <description> <command...>: retry every 2s until the command succeeds.
wait_for() {
  local timeout="$1" what="$2"; shift 2
  local deadline=$((SECONDS + timeout))
  until "$@" >/dev/null 2>&1; do
    (( SECONDS >= deadline )) && die "timed out after ${timeout}s waiting for: $what"
    sleep 2
  done
}

# render <template> <VAR...>: envsubst restricted to the named variables, which must be set.
render() {
  local tmpl="$1"; shift
  local v list=""
  for v in "$@"; do
    [[ -n "${!v:-}" ]] || die "render $tmpl: \$$v is not set"
    list+="\${$v} "
  done
  envsubst "$list" <"$tmpl"
}

_port_listening() { nc -z localhost "$1" >/dev/null 2>&1; }

# port_forward <name> <namespace> <resource> <local-port> <remote-port>
# Reuses a live forward we started, restarts a dead one, refuses a port owned by someone else.
port_forward() {
  local name="$1" ns="$2" res="$3" lport="$4" rport="$5"
  local pidf="$STATE_DIR/pf-$name.pid"
  if [[ -f "$pidf" ]] && kill -0 "$(cat "$pidf")" 2>/dev/null; then
    _port_listening "$lport" && return 0
    kill "$(cat "$pidf")" 2>/dev/null || true
  fi
  rm -f "$pidf"
  _port_listening "$lport" && die "port $lport is in use by another process; stop it or free the port"
  kubectl -n "$ns" port-forward "$res" "$lport:$rport" >"$STATE_DIR/pf-$name.log" 2>&1 &
  echo $! >"$pidf"
  wait_for 20 "port-forward $name on :$lport" _port_listening "$lport"
}

stop_port_forwards() {
  local f
  for f in "$STATE_DIR"/pf-*.pid; do
    [[ -f "$f" ]] || continue
    kill "$(cat "$f")" 2>/dev/null || true
    rm -f "$f"
  done
}

# --- Substrate helpers (idempotent) ---

ensure_atespace() {
  kubectl ate get atespace "$1" >/dev/null 2>&1 || kubectl ate create atespace "$1"
}

# ensure_template <atespace> <name> <rendered-yaml-file>: create once, then wait for the golden snapshot.
ensure_template() {
  local as="$1" name="$2" file="$3"
  kubectl ate get actor-template "$name" -a "$as" >/dev/null 2>&1 || kubectl ate create actor-template -f "$file"
  wait_for 600 "golden snapshot for template $as/$name" _template_ready "$as" "$name"
}
_template_ready() {
  kubectl ate get actor-template "$2" -a "$1" -o json | jq -e '.status.goldenSnapshotStatus.goldenTag.name' >/dev/null
}

ensure_actor() {
  local as="$1" name="$2" template="$3"
  actor_json "$as" "$name" >/dev/null 2>&1 || kubectl ate create actor "$name" --template="$template" -a "$as"
}

actor_json()   { kubectl ate get actor "$2" -a "$1" -o json; }
# actor_state strips the ACTOR_STATE_ prefix (e.g. ACTOR_STATE_RUNNING -> RUNNING) so
# callers can compare against bare RUNNING/SUSPENDED.
actor_state()  { actor_json "$1" "$2" | jq -r '.status.state // "UNKNOWN"' | sed 's/^ACTOR_STATE_//'; }
actor_worker() { actor_json "$1" "$2" | jq -r '.status.workerAssignment.workerPod // ""'; }

# router_curl <actor> [curl args...]: talk to an actor through a port-forwarded atenet-router.
router_curl() {
  local actor="$1"; shift
  port_forward router ate-system svc/atenet-router 8080 80
  curl -sS --max-time 90 -H "ate-target-actor: ${ATESPACE}/${actor}" "$@"
}

_actor_in_state() { [[ "$(actor_state "$1" "$2")" == "$3" ]]; }

# suspend_actor <atespace> <name>: checkpoint a running actor and wait until it is SUSPENDED.
suspend_actor() {
  local as="$1" name="$2"
  if [[ "$(actor_state "$as" "$name")" == RUNNING ]]; then
    kubectl ate suspend actor "$name" -a "$as" >/dev/null
  fi
  wait_for 120 "actor $as/$name to be SUSPENDED" _actor_in_state "$as" "$name" SUSPENDED
}

# _actors_list <atespace>: the actor list as a JSON array, whatever envelope kubectl ate uses.
_actors_list() {
  kubectl ate get actors -a "$1" -o json | jq -c 'if type == "array" then . else (.actors // .items // []) end'
}
actor_names() { _actors_list "$1" | jq -r '.[].metadata.name'; }

# count_actors_in_state <atespace> <STATE> [name-prefix]
count_actors_in_state() {
  _actors_list "$1" | jq --arg s "$2" --arg p "${3:-}" \
    '[.[] | select(.status.state == $s and (.metadata.name | startswith($p)))] | length'
}

# gateway_chat <actor> <message>: one chat turn through the gateway's front door.
# Prints the JSON body, then the HTTP status code on its own last line.
gateway_chat() {
  port_forward gateway "$NAMESPACE" svc/gateway 8081 80
  curl -sS --max-time 150 -H 'Content-Type: application/json' -w '\n%{http_code}\n' \
    -d "$(jq -cn --arg m "$2" '{message: $m}')" "http://localhost:8081/chat/$1"
}
