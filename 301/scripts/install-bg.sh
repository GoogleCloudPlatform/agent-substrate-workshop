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

# install-bg.sh          start background Agent Substrate v1.0.0 + prod-sim setup
# install-bg.sh --status report whether the background setup is running, finished, or failed
source "$(dirname "$0")/lib.sh"
LOG="$STATE_DIR/install.log" PIDF="$STATE_DIR/install.pid" DONE="$STATE_DIR/install-complete"
mkdir -p "$STATE_DIR"

if [[ "${1:-}" == --worker ]]; then
  load_env
  "$REPO_ROOT/scripts/install-substrate.sh"
  if [[ ! -s "$STATE_DIR/ledger-api-image" || ! -s "$STATE_DIR/ops-hub-image" ]] ||
     ! kubectl -n "$SIM_NAMESPACE" get deploy ops-hub ledger-api >/dev/null 2>&1; then
    "$REPO_ROOT/scripts/prod-sim.sh"
  fi
  state_put install-complete "$(date -u +%Y-%m-%dT%H:%M:%SZ) SUBSTRATE_REF=$SUBSTRATE_REF PROD_SIM=ready"
  exit 0
fi

if [[ "${1:-}" != --status ]]; then
  install_running && die "an install is already running (pid $(cat "$PIDF")); run: make install-status"
  nohup bash "$REPO_ROOT/scripts/install-bg.sh" --worker >"$LOG" 2>&1 </dev/null &
  echo $! >"$PIDF"
  ok "installing Agent Substrate and deploying prod-sim in the background (pid $!); check progress with: make install-status"
  exit 0
fi

[[ -f "$LOG" ]] && tail -n 20 "$LOG"
echo
if install_running; then
  ok "install running (pid $(cat "$PIDF")); run make install-status again in a minute"
elif [[ -s "$DONE" && ( ! -f "$PIDF" || "$DONE" -nt "$PIDF" ) ]]; then
  ok "install finished: $(cat "$DONE")"
elif [[ -f "$PIDF" ]]; then
  if [[ -f "$LOG" ]] && grep -q 'no space left on device' "$LOG"; then
    warn "the install ran out of disk space; run go clean -cache, keep SUBSTRATE_IMAGE_REPO set in workshop.env, then run: make install-bg"
  fi
  die "the install is not running and did not finish; inspect .workshop/install.log and re-run: make install-bg"
else
  warn "no background install has been started; run: make install-bg"
fi

if [[ -s "$STATE_DIR/reasoning-engine-id" ]]; then
  ok "managed session/memory runtime ready: $(state_get reasoning-engine-id)"
fi
if [[ -s "$STATE_DIR/ledger-api-image" && -s "$STATE_DIR/ops-hub-image" ]]; then
  ok "prod-sim images built and deployed (ledger-api + ops-hub)"
fi
user_has_ate || path_hint
