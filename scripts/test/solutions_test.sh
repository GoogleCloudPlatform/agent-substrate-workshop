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

# Every module solution must compile as app/cmd/agent/main.go, and the starter must carry
# exactly one WORKSHOP marker per guided edit.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
fails=0
markers="$(grep -c '// WORKSHOP(module-[234]):' "$ROOT/app/cmd/agent/main.go" || true)"
if [[ "$markers" == 3 ]]; then echo "ok   starter has 3 markers"; else echo "FAIL starter has $markers markers, want 3"; fails=$((fails+1)); fi
for sol in "$ROOT"/modules/*/solution/main.go; do
  rm -rf "$TMP/app" && cp -R "$ROOT/app" "$TMP/app"
  cp "$sol" "$TMP/app/cmd/agent/main.go"
  if (cd "$TMP/app" && go build -o /dev/null ./cmd/agent); then echo "ok   ${sol#"$ROOT"/}"; else echo "FAIL ${sol#"$ROOT"/}"; fails=$((fails+1)); fi
done
exit "$fails"
