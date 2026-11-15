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

# Offline repository hygiene, branding, license header, and security assertions.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT" || exit 1
export LC_ALL=C

fails=0
check() { local name="$1"; shift; if "$@"; then echo "ok   $name"; else echo "FAIL $name"; fails=$((fails+1)); fi; }

tracked_files() {
  {
    git ls-files
    git ls-files --others --exclude-standard
  } | sort -u | while IFS= read -r f; do
    [[ -f "$f" ]] && printf '%s\n' "$f"
  done
}

# Construct forbidden patterns dynamically so this script never matches itself.
t_no_personal_identifiers() {
  local p1="mofi""labs"
  local p2="mofi""codes"
  local p3="gke-""ate-cluster-"
  local p4="fluid ""compute"
  local bad=0 f
  while IFS= read -r f; do
    if grep -Ein "${p1}|${p2}|${p3}|${p4}" "$f" >&2; then
      echo "  forbidden identifier in $f" >&2
      bad=1
    fi
  done < <(tracked_files)
  return "$bad"
}

t_no_retired_branding() {
  local retired="Ver""tex([[:space:]]+AI|[^a-zA-Z0-9_.-]|$)"
  local bad=0 f
  while IFS= read -r f; do
    if grep -En "$retired" "$f" >&2; then
      echo "  retired platform branding in $f" >&2
      bad=1
    fi
  done < <(tracked_files)
  return "$bad"
}

t_no_unsafe_dom_or_browser_token_storage() {
  local dom_pat="inner""HTML|outer""HTML|insertAdjacent""HTML|document\\.write"
  local store_pat="local""Storage|session""Storage"
  local dialog_pat="(^|[^a-zA-Z0-9_])(alert|confirm|prompt)\\("
  local bad=0 f
  while IFS= read -r f; do
    [[ "$f" == "scripts/test/hygiene_test.sh" ]] && continue
    [[ "$f" == "AGENTS.md" || "$f" == "DESIGN.md" || "$f" == docs/* ]] && continue
    if grep -En "${dom_pat}|${store_pat}" "$f" >&2; then
      echo "  unsafe DOM or browser storage usage in $f" >&2
      bad=1
    fi
    if [[ "$f" == *.js ]] && grep -En "$dialog_pat" "$f" >&2; then
      echo "  blocking browser dialog in $f" >&2
      bad=1
    fi
  done < <(tracked_files)
  return "$bad"
}

t_no_wildcard_test_listeners() {
  local wildcard="0\\.0\\.0\\.0"
  local bad=0 f
  while IFS= read -r f; do
    case "$f" in
      *_test.go|scripts/test/*.sh)
        [[ "$f" == "scripts/test/hygiene_test.sh" ]] && continue
        if grep -En "$wildcard" "$f" >&2; then
          echo "  wildcard 0.0.0.0 listener in test file $f" >&2
          bad=1
        fi ;;
    esac
  done < <(tracked_files)
  return "$bad"
}

t_apache_license_headers() {
  local bad=0 f
  while IFS= read -r f; do
    case "$f" in
      *.go|*.sh|*.tmpl|*.svg|Makefile|workshop.env.example|app/.ko.yaml)
        if ! head -n 5 "$f" | grep -q "Copyright 2026 Google LLC"; then
          echo "  missing Apache 2.0 header in $f" >&2
          bad=1
        fi ;;
    esac
  done < <(tracked_files)
  return "$bad"
}

t_no_invalid_kubectl_ate_jsonpath() {
  local pat="kubectl"" ate .*jsonpath"
  local bad=0 f
  while IFS= read -r f; do
    [[ "$f" == "scripts/test/hygiene_test.sh" ]] && continue
    if grep -En "$pat" "$f" >&2; then
      echo "  unsupported kubectl ate -o jsonpath in $f (use -o json | jq instead)" >&2
      bad=1
    fi
  done < <(tracked_files)
  return "$bad"
}

check "zero personal project, org, node, or internal compute terms" t_no_personal_identifiers
check "zero retired platform branding" t_no_retired_branding
check "zero unsafe DOM APIs or localStorage/sessionStorage usage" t_no_unsafe_dom_or_browser_token_storage
check "zero 0.0.0.0 wildcard listeners in tests" t_no_wildcard_test_listeners
check "zero unsupported kubectl ate -o jsonpath flags" t_no_invalid_kubectl_ate_jsonpath
check "Apache 2.0 license headers on code, scripts, and templates" t_apache_license_headers
exit "$fails"
