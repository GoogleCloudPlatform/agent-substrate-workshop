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

# Turns the gateway's idle-suspender off or on (Module 5), for labs that need an actor to stay RUNNING
# between commands. `off` sets IDLE_AFTER=0s (internal/idle: IdleAfter <= 0 is a no-op); `on` restores
# IDLE_AFTER (default 3s). Either way the gateway rolls, so the localhost:8081 port-forward is restarted.
#   scripts/idle.sh off | on      (make idle-off / make idle-on [IDLE_AFTER=3s])
source "$(dirname "$0")/lib.sh"
load_env
case "${1:-}" in
  off) value=0s ;;
  on) value="${IDLE_AFTER:-3s}" ;;
  *) die "usage: $0 off|on" ;;
esac
step "gateway IDLE_AFTER=$value"
kubectl -n "$NAMESPACE" set env deploy/gateway "IDLE_AFTER=$value" >/dev/null
kubectl -n "$NAMESPACE" rollout status deploy/gateway --timeout=180s >/dev/null
# Wait for the replaced gateway pod's 3s drain sleep to finish so kubectl port-forward and
# atenet-egress never talk to a pod that is about to close its listener.
_no_terminating_gateway() {
  [[ -z "$(kubectl -n "$NAMESPACE" get pods -l app=gateway -o jsonpath='{.items[*].metadata.deletionTimestamp}' 2>/dev/null)" ]]
}
wait_for 30 "old gateway pod to terminate" _no_terminating_gateway
stop_port_forward gateway
port_forward gateway "$NAMESPACE" svc/gateway 8081 80
if [[ "$value" == 0s ]]; then
  ok "idle-suspend is off: actors stay RUNNING until you suspend them (make idle-on to restore)"
else
  ok "idle-suspend is on: actors are suspended after $value without a request or heartbeat"
fi
