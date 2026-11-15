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

# Bursty chat traffic across the swarm through the gateway, so the dashboards have something to show.
# Env: DURATION (s, default 180), CONCURRENCY (default 2 = worker count), ACTORS (default swarm-01..20).
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_env
: "${DURATION:=180}" "${CONCURRENCY:=2}"
export DURATION CONCURRENCY
read -r -a actors <<<"${ACTORS:-$(printf 'swarm-%02d ' $(seq 1 20))}"
port_forward gateway "$NAMESPACE" svc/gateway 8081 80
out="$STATE_DIR/load-results.txt"
: >"$out"
step "chatting with ${#actors[@]} actors for ${DURATION}s, $CONCURRENCY at a time (results: $out)"
deadline=$((SECONDS + DURATION))
i=0
questions=("What is a snapshot?" "Name one planet." "Say hi in French." "What is 2+2?" "Give me a haiku about idle workers.")
while (( SECONDS < deadline )); do
  pids=()
  for ((c = 0; c < CONCURRENCY; c++)); do
    a="${actors[$(( (i + c) % ${#actors[@]} ))]}"
    q="${questions[$(( RANDOM % ${#questions[@]} ))]}"
    ( code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 150 -H 'Content-Type: application/json' \
        -d "$(jq -cn --arg m "$q" '{message:$m}')" "http://localhost:8081/chat/$a")"
      echo "$a $code" >>"$out"; echo "  $a -> $code" ) &
    pids+=("$!")
  done
  # Wait only on this burst's own PIDs. A bare `wait` would also block on the
  # port_forward's kubectl process, which stays running for the whole script.
  (( ${#pids[@]} )) && wait "${pids[@]}"
  i=$((i + CONCURRENCY))
  sleep $(( RANDOM % 4 ))
done
step "status codes"
awk '{print $2}' "$out" | sort | uniq -c
