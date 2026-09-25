#!/usr/bin/env bash
# Module 5 end state: 20 swarm actors on 2 workers; the idle-suspender is on and has suspended actors;
# 3 concurrent chats all succeed (one parks until a worker frees up).
source "$(dirname "$0")/common.sh"
step "verify module 5 (density and idle)"
check "20 swarm actors exist" test "$(actor_names "$ATESPACE" | grep -c '^swarm-')" -eq 20

# A failed jsonpath lookup must not crash the script under set -e; it should just leave $idle
# empty so the check below reports a FAIL line instead of an uncaught exit.
idle="$(kubectl -n "$NAMESPACE" get deploy gateway \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="IDLE_AFTER")].value}' 2>/dev/null)" || true
check "gateway idle-suspender is on (IDLE_AFTER=$idle)" bash -c "[[ -n '$idle' && '$idle' != 0s ]]"
check "swarm-20 has an egress policy" bash -c "'$REPO_ROOT/scripts/egress.sh' get --actor=swarm-20 | grep -q gateway"

no_swarm_running() { [[ "$(count_actors_in_state "$ATESPACE" RUNNING swarm-)" == 0 ]]; }
# wait_for dies on timeout; the subshell keeps that from ending the verify run.
( wait_for 120 "idle swarm actors to be suspended" no_swarm_running ) || warn "some swarm actors are still RUNNING"

port_forward gateway "$NAMESPACE" svc/gateway 8081 80
pids=()
for a in swarm-07 swarm-08 swarm-09; do
  curl -s -o /dev/null -w '%{http_code}\n' --max-time 150 -H 'Content-Type: application/json' \
    -d '{"message":"verify: say ok"}' "http://localhost:8081/chat/$a" >"$STATE_DIR/v05-$a" &
  pids+=("$!")
done
# Wait only on this burst's own PIDs. A bare `wait` would also block on port_forward's kubectl
# process, which stays running for the rest of the verify run. Guard against an empty array,
# which bash 3.2 rejects under `set -u`.
(( ${#pids[@]} )) && wait "${pids[@]}"
codes="$(cat "$STATE_DIR"/v05-swarm-0{7,8,9} 2>/dev/null | tr '\n' ' ')" || true
codes="${codes% }"
check "3 concurrent chats on 2 workers all return 200 ($codes)" bash -c "[[ '$codes' == '200 200 200' ]]"

idle_suspends() { curl -s localhost:8081/metrics | awk '/^workshop_idle_suspends_total\{outcome="ok"\}/ {print int($2)}'; }
check "the gateway has idle-suspended actors" test "$(idle_suspends || echo 0)" -gt 0
check "at least 15 swarm actors are SUSPENDED" test "$(count_actors_in_state "$ATESPACE" SUSPENDED swarm-)" -ge 15
finish 5
