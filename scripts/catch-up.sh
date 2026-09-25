#!/usr/bin/env bash
# catch-up.sh N — bring your environment to the start of module N (the end of module N-1).
# Safe to re-run: every step skips work that is already done. Your main.go is backed up before it is replaced.
source "$(dirname "$0")/lib.sh"
load_env
N="${1:-}"
[[ "$N" =~ ^[1-7]$ ]] || die "usage: make catch-up MODULE=<1-7>"
S="$REPO_ROOT/scripts" M="$REPO_ROOT/modules" MAIN="$REPO_ROOT/app/cmd/agent/main.go"
POLICY="$M/04-make-it-an-agent/manifests/egress-gateway.yaml"

use_solution() {
  local sol="$M/$1/solution/main.go"
  cmp -s "$sol" "$MAIN" && return 0
  cp "$MAIN" "$STATE_DIR/main.go.bak.$(date +%s)"
  cp "$sol" "$MAIN"
  ok "app/cmd/agent/main.go ← modules/$1/solution/main.go (yours is backed up in .workshop/)"
}
# stage_template <module-dir> <template>: build that module's agent and create its template, once.
stage_template() {
  use_solution "$1"
  if ! kubectl ate get actor-template "$2" -a "$ATESPACE" >/dev/null 2>&1; then
    "$S/build.sh" agent
    "$S/render.sh" "$M/$1/manifests/template.yaml.tmpl" >"$STATE_DIR/$2.yaml"
  fi
  ensure_template "$ATESPACE" "$2" "$STATE_DIR/$2.yaml"
  ok "template $2 ready"
}
_no_bg_install() { ! pgrep -f scripts/install-substrate.sh >/dev/null; }
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
if [[ ! -s "$STATE_DIR/worker-image" ]] || ! kubectl -n ate-system get deploy atenet-router >/dev/null 2>&1; then
  "$S/install-substrate.sh"
fi
"$S/dashboards.sh"
make -s -C "$REPO_ROOT" tools

if (( N >= 3 )); then
  step "module 2 end state: WorkerPool, atespace, agent-v1, hello"
  "$S/render.sh" "$M/02-hello-actor/manifests/workerpool.yaml.tmpl" | kubectl apply -f -
  wait_for 600 "2 running worker pods" _two_workers
  ensure_atespace "$ATESPACE"
  stage_template 02-hello-actor agent-v1
  ensure_actor "$ATESPACE" hello agent-v1
  if (( N == 3 )); then router_curl hello -f http://localhost:8080/ >/dev/null; fi
fi

if (( N >= 4 )); then
  step "module 3 end state: agent-v2, memo"
  suspend_actor "$ATESPACE" hello       # the golden actor for agent-v2 needs a free worker
  stage_template 03-suspend-resume agent-v2
  ensure_actor "$ATESPACE" memo agent-v2
  if (( N == 4 )); then router_curl memo -f http://localhost:8080/ >/dev/null; fi
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
      gateway_chat buddy "hello from catch-up" >/dev/null
    fi
  fi
fi

if (( N >= 6 )); then
  step "module 5 end state: 20 swarm actors, atespace-wide policy, idle-suspend on"
  suspend_actor "$ATESPACE" buddy       # module 5 step 1: clear the decks
  for i in $(seq -w 1 20); do _exists "swarm-$i" || kubectl ate create actor "swarm-$i" --template=agent-v3 -a "$ATESPACE"; done
  "$S/egress.sh" apply "$POLICY" --all
  IDLE_AFTER=20s "$S/gateway.sh"
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
