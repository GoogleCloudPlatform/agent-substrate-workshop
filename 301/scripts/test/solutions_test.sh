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

# Verifies that starter app/cmd/agent/main.go carries all 4 progressive WORKSHOP(module-1..4)
# markers, that every module solution preserves downstream markers word-for-word, and that
# starter + every solution compile cleanly.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
fails=0

markers="$(grep -c '// WORKSHOP(module-[1234]):' "$ROOT/app/cmd/agent/main.go" || true)"
if [[ "$markers" == 4 ]]; then
  echo "ok   starter app/cmd/agent/main.go has 4 WORKSHOP(module-1..4) markers"
else
  echo "FAIL starter app/cmd/agent/main.go has $markers markers, want 4"
  fails=$((fails + 1))
fi

shopt -s nullglob
solutions=("$ROOT"/modules/*/solution/main.go)
shopt -u nullglob
if (( ${#solutions[@]} == 0 )); then
  echo "FAIL no modules/*/solution/main.go files found"
  exit 1
fi

for sol in "${solutions[@]}"; do
  mod="$(basename "$(dirname "$(dirname "$sol")")")"
  n=$((10#${mod%%-*}))
  while IFS= read -r line; do
    id="$(sed -E 's|.*// WORKSHOP\(([^)]*)\):.*|\1|' <<<"$line")"
    [[ "$id" == module-* ]] && (( ${id#module-} <= n )) && continue
    if grep -qxF -- "$line" "$sol"; then
      echo "ok   ${sol#"$ROOT"/} keeps WORKSHOP($id)"
    else
      echo "FAIL ${sol#"$ROOT"/} lost or altered the WORKSHOP($id) marker"
      fails=$((fails + 1))
    fi
  done < <(grep '// WORKSHOP(' "$ROOT/app/cmd/agent/main.go")
done

if (cd "$ROOT/app" && go build -o /dev/null ./cmd/agent && go build -o /dev/null ./cmd/sre-gateway); then
  echo "ok   starter cmd/agent and cmd/sre-gateway compile"
else
  echo "FAIL starter cmd/agent or cmd/sre-gateway failed to compile"
  fails=$((fails + 1))
fi

for sol in "${solutions[@]}"; do
  rm -rf "$TMP/app" && cp -R "$ROOT/app" "$TMP/app"
  cp "$sol" "$TMP/app/cmd/agent/main.go"
  if (cd "$TMP/app" && go build -o /dev/null ./cmd/agent); then
    echo "ok   ${sol#"$ROOT"/} compiles"
  else
    echo "FAIL ${sol#"$ROOT"/} failed to compile"
    fails=$((fails + 1))
  fi
done

exit "$fails"
