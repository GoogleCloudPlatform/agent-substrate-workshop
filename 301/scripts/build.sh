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

# Builds a workshop container image via ko into Artifact Registry and records its
# digest-pinned reference in .workshop/<state-key>.
source "$(dirname "$0")/lib.sh"
load_env

target="${1:-}"
case "$target" in
  agent)
    cmd_dir="./cmd/agent"
    state_key="agent-image"
    ;;
  sre-gateway|gateway)
    cmd_dir="./cmd/sre-gateway"
    state_key="gateway-image"
    ;;
  *)
    die "usage: build.sh agent|sre-gateway [--check-changed]"
    ;;
esac

export KO_DOCKER_REPO KO_DEFAULTPLATFORMS
step "ko build ${cmd_dir#./cmd/} -> ${KO_DOCKER_REPO}"
prev="$(state_get "$state_key" || true)"
ref="$(cd "$REPO_ROOT/app" && ko build --base-import-paths "$cmd_dir")"
[[ "$ref" == *@sha256:* ]] || die "ko did not return a digest-pinned image reference: $ref"
state_put "$state_key" "$ref"
ok "$ref (saved to .workshop/$state_key)"

if [[ "${2:-}" == "--check-changed" && "$ref" == "$prev" ]]; then
  warn "image digest matches your previous ${cmd_dir#./cmd/} build. If this step asked you to edit app/cmd/agent/main.go, complete its WORKSHOP(...) section, save the file, and run make build-agent again."
fi
