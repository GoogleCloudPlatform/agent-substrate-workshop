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

# Load workshop.env into your interactive bash or zsh shell without enabling errexit:
#   source scripts/env.sh
if [ ! -f scripts/print-env.sh ]; then
  echo "run this from the workshop repo root: source scripts/env.sh" >&2
  return 1
fi
if _workshop_env="$(bash scripts/print-env.sh)"; then
  eval "$_workshop_env"
  unset _workshop_env
  case ":$PATH:" in
    *":$REPO_ROOT/bin:"*) ;;
    *) export PATH="$REPO_ROOT/bin:$PATH" ;;
  esac
  export WORKSHOP_USER_PATH="$PATH"
  complete_3lo_pkce_grant() {
    bash -c 'source "$1/scripts/lib.sh" && load_env && complete_3lo_pkce_grant "${@:2}"' _ "$REPO_ROOT" "$@"
  }
else
  unset _workshop_env
  return 1
fi
