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

# Prints `export NAME=value` lines for workshop.env (after load_env's defaults) plus REPO_ROOT.
# scripts/env.sh evals this in your shell; run it on its own to see what that would set.
source "$(dirname "$0")/lib.sh"
load_env
f="${WORKSHOP_ENV_FILE:-$REPO_ROOT/workshop.env}"
names=(REPO_ROOT NAMESPACE ATESPACE MOCK_LLM CLUSTER_NAME CLUSTER_LOCATION KO_DEFAULTPLATFORMS)
while IFS= read -r n; do names+=("$n"); done < <(sed -n 's/^[[:space:]]*\(export[[:space:]]\{1,\}\)\{0,1\}\([A-Za-z_][A-Za-z0-9_]*\)=.*/\2/p' "$f")
for n in $(printf '%s\n' "${names[@]}" | awk '!seen[$0]++'); do
  # %q quotes for bash; zsh reads the same quoting ($'…' included).
  printf 'export %s=%q\n' "$n" "${!n-}"
done
