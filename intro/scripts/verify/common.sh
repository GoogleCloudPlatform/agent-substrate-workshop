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

# Shared by scripts/verify/NN.sh. Each check prints ✓ or FAIL; finish exits non-zero if any failed.
source "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
load_env
VFAILS=0

# check <description> <command...>: run the command quietly and record the result.
check() {
  local what="$1"; shift
  if "$@" >/dev/null 2>&1; then ok "$what"; else warn "FAIL: $what"; VFAILS=$((VFAILS + 1)); fi
}

finish() {
  local next=$(( $1 + 1 )) reset=""
  # Modules 2-6 create templates/actors; catch-up only creates missing ones unless RESET=1.
  (( next >= 3 )) && reset=" — if a template or actor you created is broken, add RESET=1 to delete and recreate it and its actors"
  (( VFAILS == 0 )) || die "module $1: $VFAILS check(s) failed — see instructor/troubleshooting.md, or run: make catch-up MODULE=$next$reset"
  ok "module $1 complete"
}
