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

# catch-up.sh N — bring your environment to the start of module N (the end of module N-1).
# Safe to re-run: every step skips work that is already done. Your main.go is backed up before it is replaced.
# RESET=1 also deletes the newest template catch-up N stages (N=3: agent-v1, N=4: agent-v2, N>=5: agent-v3)
# and every actor made from it, in any state, then rebuilds and recreates them from the solution.
# Use it when a template exists but is broken (a golden snapshot is immutable, so it can't be fixed in place).
# Note: past Module 5 the gateway's IDLE_AFTER is live. Re-running catch-up at a lower N doesn't
# turn it off, so actors can still get idle-suspended while you work back through earlier modules.
source "$(dirname "$0")/lib.sh"
load_env
N="${1:-}"
[[ "$N" =~ ^[1-7]$ ]] || die "usage: make catch-up MODULE=<1-7> [RESET=1]"
RESET_TEMPLATE=""
if [[ "${RESET:-}" == 1 ]]; then
  case "$N" in
    1|2) die "RESET=1 needs MODULE=3 or later: modules 0 and 1 create no template" ;;
    3)   RESET_TEMPLATE=agent-v1 ;;
    4)   RESET_TEMPLATE=agent-v2 ;;
    *)   RESET_TEMPLATE=agent-v3 ;;
  esac
fi
S="$REPO_ROOT/scripts" M="$REPO_ROOT/modules" MAIN="$REPO_ROOT/app/cmd/agent/main.go"
POLICY="$M/04-make-it-an-agent/manifests/egress-gateway.yaml"
BACKED_UP=false

# is_known_solution: true if $MAIN already matches one of our own modules/*/solution/main.go
# files, i.e. it's leftover from a previous catch-up run rather than the attendee's own code.
is_known_solution() {
  local f
  for f in "$M"/*/solution/main.go "$REPO_ROOT"/optional/*/solution/main.go; do
    cmp -s "$f" "$MAIN" && return 0
  done
  return 1
}
use_solution() {
  local sol="$M/$1/solution/main.go"
  cmp -s "$sol" "$MAIN" && return 0
  # Back up at most once per run, and only the attendee's own code — not a solution file this
  # same run already wrote (stage_template can call use_solution up to three times when
  # catching up several modules at once; a second/third backup would otherwise overwrite the
  # one backup that matters with a copy of our own solution code).
  if ! $BACKED_UP && ! is_known_solution; then
    local bak
    bak="$STATE_DIR/main.go.bak.$(date +%Y%m%d-%H%M%S).$$"
    cp "$MAIN" "$bak"
    BACKED_UP=true
    ok "backed up your app/cmd/agent/main.go to $bak"
  fi
  cp "$sol" "$MAIN"
  ok "app/cmd/agent/main.go ← modules/$1/solution/main.go"
}
# stage_template <module-dir> <template>: build that module's agent and create its template, once.
# With RESET=1, the template being reset (and its actors) is deleted first, so it is always rebuilt.
stage_template() {
  use_solution "$1"
  if [[ "$2" == "$RESET_TEMPLATE" ]]; then
    step "RESET=1: deleting template $2 and all its actors (any state), to recreate them from the solution"
    reset_template "$ATESPACE" "$2"
    ok "template $2 and its actors deleted"
  fi
  if ! kubectl ate get actor-template "$2" -a "$ATESPACE" >/dev/null 2>&1; then
    "$S/build.sh" agent
    "$S/render.sh" "$M/$1/manifests/template.yaml.tmpl" >"$STATE_DIR/$2.yaml"
  fi
  ensure_template "$ATESPACE" "$2" "$STATE_DIR/$2.yaml"
  ok "template $2 ready"
}
# Tightened past a bare `install-substrate.sh` substring match so this doesn't also match,
# say, an editor buffer or a grep for the same string in someone's shell history.
_no_bg_install() { ! install_running && ! pgrep -f 'bash .*scripts/install-substrate.sh' >/dev/null; }
_two_workers() {
  [[ "$(kubectl -n "$NAMESPACE" get pods -l workload=workshop-agent --field-selector=status.phase=Running -o name | wc -l)" -ge 2 ]]
}
_exists() { actor_json "$ATESPACE" "$1" >/dev/null 2>&1; }

step "catching up to the start of module $N"

# End of module 0 (and 1, which only tours the install).
"$S/preflight.sh"
"$S/gcp-resources.sh"
if ! _no_bg_install; then
  step "a background install is running — waiting for it (tail -f .workshop/install.log)"
  wait_for 1800 "background install to finish" _no_bg_install
fi
# install-substrate.sh writes .workshop/install-complete as its very last step (after node
# labeling and the router patch), so that single marker is what verify/01.sh's checks actually
# depend on having happened. A missing marker on an otherwise-installed cluster (e.g. .workshop/
# predates this marker, or was partially cleaned up) just means we re-run it — install-substrate.sh
# is safe to re-run (its own header says so, and every step in it is create-or-patch-if-missing).
if [[ ! -s "$STATE_DIR/install-complete" ]]; then
  "$S/install-substrate.sh"
fi
"$S/dashboards.sh"
make -s -C "$REPO_ROOT" tools

if (( N >= 3 )); then
  step "module 2 end state: WorkerPool, atespace, agent-v1, hello"
  if kubectl -n "$NAMESPACE" get workerpool agents >/dev/null 2>&1 &&
     [[ -z "$(kubectl -n "$NAMESPACE" get workerpool agents -o jsonpath='{.spec.sandboxClasses[0].name}' 2>/dev/null)" ]]; then
    kubectl -n "$NAMESPACE" delete workerpool agents --ignore-not-found
  fi
  "$S/render.sh" "$M/02-hello-actor/manifests/workerpool.yaml.tmpl" | kubectl apply -f -
  wait_for 600 "2 running worker pods" _two_workers
  ensure_atespace "$ATESPACE"
  stage_template 02-hello-actor agent-v1
  ensure_actor "$ATESPACE" hello agent-v1
  if (( N == 3 )); then router_curl hello -f "http://localhost:${ROUTER_LOCAL_PORT:-8080}/" >/dev/null; fi
fi

if (( N >= 4 )); then
  step "module 3 end state: agent-v2, memo"
  suspend_actor "$ATESPACE" hello       # the golden actor for agent-v2 needs a free worker
  stage_template 03-suspend-resume agent-v2
  ensure_actor "$ATESPACE" memo agent-v2
  if (( N == 4 )); then router_curl memo -f "http://localhost:${ROUTER_LOCAL_PORT:-8080}/" >/dev/null; fi
fi

if (( N >= 5 )); then
  step "module 4 end state: gateway, agent-v3, buddy + egress policy"
  suspend_actor "$ATESPACE" memo
  [[ -s "$STATE_DIR/gateway-image" ]] || "$S/build.sh" gateway
  "$S/gateway.sh"
  stage_template 04-make-it-an-agent agent-v3
  ensure_actor "$ATESPACE" buddy agent-v3
  "$S/egress.sh" apply "$POLICY" --actor=buddy
  # DEVIATION: no suspend/resume here. atenet-egress refetches each actor's policy from
  # ateapi every DefaultPolicyCacheTTL (10s; substrate cmd/atenet/internal/router/egress);
  # a running actor's *next* outbound call sees the new policy on its own. Module 4 step 4
  # says the same thing. The very next call can still race the cache, so retry once.
  if (( N == 5 )); then
    out="$(gateway_chat buddy "hello from catch-up")" || true
    code="${out##*$'\n'}"
    if [[ "$code" != 200 ]]; then
      sleep 10
      out="$(gateway_chat buddy "hello from catch-up")" || true
      code="${out##*$'\n'}"
      [[ "$code" == 200 ]] || die "buddy still can't reach the gateway after the egress policy cache window (last status: $code) — see instructor/troubleshooting.md"
    fi
  fi
fi

if (( N >= 6 )); then
  step "module 5 end state: 20 swarm actors, policy on every current actor, idle-suspend on"
  suspend_actor "$ATESPACE" buddy       # module 5 step 1: clear the decks
  for i in $(seq -w 1 20); do _exists "swarm-$i" || kubectl ate create actor "swarm-$i" --template=agent-v3 -a "$ATESPACE"; done
  "$S/egress.sh" apply "$POLICY" --all
  IDLE_AFTER=3s "$S/gateway.sh"
  gateway_chat swarm-01 "hello from catch-up" >/dev/null
fi

if (( N >= 7 )); then
  step "module 6 end state: PodMonitoring + Prometheus UI"
  "$S/observability.sh"
  port_forward gateway "$NAMESPACE" svc/gateway 8081 80
  for a in swarm-02 swarm-03; do gateway_chat "$a" "hello from catch-up" >/dev/null; done
fi

moddir=""
for d in "$M"/0"$N"-*/; do moddir="${d%/}"; moddir="${moddir##*/}"; break; done
ok "ready for module $N — open modules/$moddir/README.md"
user_has_ate || path_hint
