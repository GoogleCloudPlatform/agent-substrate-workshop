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

# Module 3 end state: memo's disk counter survives a suspend while its process (bootID) is replaced.
source "$(dirname "$0")/common.sh"
step "verify module 3 (suspend/resume)"
check "template agent-v2 has a golden snapshot" _template_ready "$ATESPACE" agent-v2
check "actor memo exists" actor_json "$ATESPACE" memo

memo_state() { router_curl memo -f http://localhost:8080/state; }
if before="$(router_curl memo -f http://localhost:8080/ >/dev/null && memo_state)"; then
  w_before="$(actor_worker "$ATESPACE" memo)"
  suspend_actor "$ATESPACE" memo
  after="$(memo_state || echo '{}')"
  w_after="$(actor_worker "$ATESPACE" memo)"
  b_disk="$(jq .diskCount <<<"$before")"; a_disk="$(jq .diskCount <<<"$after")"
  check "memo writes its counter to /data (diskCount ≥ 1)" test "$b_disk" -ge 1
  check "diskCount survived the suspend ($b_disk → $a_disk)" test "$a_disk" -eq "$b_disk"
  check "memo cold-booted on resume (new bootID)" test "$(jq -r .bootID <<<"$before")" != "$(jq -r .bootID <<<"$after")"
  check "memo's in-memory count restarted (memCount 0)" test "$(jq .memCount <<<"$after")" -eq 0
  # The scheduler is free to hand memo back to the same worker it suspended from; that's not
  # a bug, so a same-worker resume WARNs instead of failing the module.
  if [[ -n "$w_after" && "$w_after" != "$w_before" ]]; then
    ok "memo resumed on a different worker ($w_before → $w_after)"
  else
    warn "memo resumed on the same worker ($w_after) — the scheduler may legitimately reuse it"
  fi
else
  check "memo answers through the router" false
fi
finish 3
