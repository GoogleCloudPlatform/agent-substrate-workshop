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

# Load workshop.env into your own shell, from the repo root, in bash or zsh:
#   source scripts/env.sh
# It exports PROJECT_ID, KO_DOCKER_REPO, BUCKET_NAME, REPO_ROOT, … with the same defaults the scripts use.
# It never changes your shell's options and never exits your shell: the work happens in a bash child
# (scripts/print-env.sh), and this file only evals the `export` lines it prints.
if [ ! -f scripts/print-env.sh ]; then
  echo "run this from the workshop repo root: source scripts/env.sh" >&2
  return 1
fi
if _workshop_env="$(bash scripts/print-env.sh)"; then
  eval "$_workshop_env"
  unset _workshop_env
else
  unset _workshop_env
  return 1
fi
