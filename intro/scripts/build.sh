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

# ko-builds a workshop binary into your Artifact Registry and records its digest-pinned ref.
# ActorTemplates reject image refs without a digest, so we always keep the @sha256 form.
# With --check-changed (make build-agent), it warns when the image is the same as your last build of it.
source "$(dirname "$0")/lib.sh"
load_env
name="${1:-}"
case "$name" in agent|gateway) ;; *) die "usage: build.sh agent|gateway [--check-changed]";; esac
export KO_DOCKER_REPO
step "ko build $name -> $KO_DOCKER_REPO"
prev="$(state_get "$name-image" || true)"
ref="$(cd "$REPO_ROOT/app" && ko build --base-import-paths "./cmd/$name")"
[[ "$ref" == *@sha256:* ]] || die "ko did not return a digest-pinned ref: $ref"
state_put "$name-image" "$ref"
ok "$ref  (saved to .workshop/$name-image)"
if [[ "${2:-}" == --check-changed && "$ref" == "$prev" ]]; then
  warn "this image is the same as your last $name build: nothing in app/ changed. If this page asks you to edit app/cmd/agent/main.go, find its WORKSHOP(...) line, save the edit, and run make build-agent again."
fi
