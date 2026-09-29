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

# install-bg.sh           start scripts/install-substrate.sh in the background (make install-bg)
# install-bg.sh --status  last log lines, then whether it's running, finished, or died (make install-status)
# The pid lands in .workshop/install.pid. install-substrate.sh writes .workshop/install-complete as its
# last step, so "not running, and no install-complete newer than the pid file" means it died.
source "$(dirname "$0")/lib.sh"
LOG="$STATE_DIR/install.log" PIDF="$STATE_DIR/install.pid" DONE="$STATE_DIR/install-complete"
mkdir -p "$STATE_DIR"

if [[ "${1:-}" != --status ]]; then
  install_running && die "an install is already running (pid $(cat "$PIDF")) — make install-status"
  nohup "$REPO_ROOT/scripts/install-substrate.sh" >"$LOG" 2>&1 </dev/null &
  echo $! >"$PIDF"
  ok "installing in the background (pid $!) — make install-status, or: tail -f .workshop/install.log"
  exit 0
fi

[[ -f "$LOG" ]] && tail -n 20 "$LOG"
echo
if install_running; then
  ok "install running (pid $(cat "$PIDF")); run make install-status again in a minute"
elif [[ -s "$DONE" && ( ! -f "$PIDF" || "$DONE" -nt "$PIDF" ) ]]; then
  ok "install finished: $(cat "$DONE")"
elif [[ -f "$PIDF" ]]; then
  # Closing the terminal can take a background job with it on some setups; so can a failed step.
  die "the install is not running and did not finish — read the log above, then re-run: make install-bg"
else
  warn "no background install has been started — run: make install-bg"
fi
user_has_ate || path_hint
