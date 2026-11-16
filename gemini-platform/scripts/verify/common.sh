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

# Shared verification helpers sourced by scripts/verify/NN.sh.
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
load_env
VFAILS=0

check() {
  local what="$1"; shift
  if "$@" >/dev/null 2>&1; then ok "$what"; else warn "FAIL: $what"; VFAILS=$((VFAILS + 1)); fi
}

finish() {
  local next=$(( $1 + 1 )) reset=""
  (( next >= 2 )) && reset=" (add RESET=1 to recreate broken actor templates)"
  (( VFAILS == 0 )) || die "module $1: $VFAILS check(s) failed; run: make catch-up MODULE=$next$reset"
  ok "module $1 complete"
}
