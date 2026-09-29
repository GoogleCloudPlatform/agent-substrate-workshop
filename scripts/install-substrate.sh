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

# Installs Agent Substrate at SUBSTRATE_REF. Published images if SUBSTRATE_IMAGE_REPO/TAG are set,
# otherwise builds from source with ko into KO_DOCKER_REPO. Safe to re-run.
source "$(dirname "$0")/lib.sh"
load_env
require_cluster_location
SRC="$STATE_DIR/substrate"
# KO_DEFAULTPLATFORMS (load_env: linux/amd64 by default) replaces substrate's .ko.yaml default of
# amd64 + arm64, which would build every image twice for a cluster that runs only one of them.
# ate-setup also passes it to docker buildx for the Envoy image.
export KO_DOCKER_REPO KO_DEFAULTPLATFORMS
# Guard against a go.work in a parent directory (the repo no longer ships one): substrate is its own module.
export GOWORK=off

step "Substrate checkout @ ${SUBSTRATE_REF}"
if [[ ! -d "$SRC/.git" ]]; then
  git clone --quiet https://github.com/agent-substrate/substrate "$SRC"
fi
git -C "$SRC" fetch --quiet --tags origin
git -C "$SRC" checkout --quiet "$SUBSTRATE_REF"
ok "$(git -C "$SRC" describe --tags --always)"

step "kubectl-ate"
mkdir -p "$REPO_ROOT/bin"
(cd "$SRC" && go build -ldflags "$(make -s ldflags | tr '\n' ' ')" -o "$REPO_ROOT/bin/kubectl-ate" ./cmd/kubectl-ate)
ok "bin/kubectl-ate (kubectl ate …)"
user_has_ate || path_hint

step "Substrate version label"
if [[ -n "${SUBSTRATE_IMAGE_TAG:-}" ]]; then
  version="$(cd "$SRC" && go run ./internal/versionlabel/cmd "$SUBSTRATE_IMAGE_TAG" | awk '{print $1}')"
else
  raw="$(cd "$SRC" && make -s ldflags | grep 'internal/version\.Version=' | head -n1)"
  version="$(cd "$SRC" && go run ./internal/versionlabel/cmd "${raw#*internal/version.Version=}" | awk '{print $1}')"
fi
state_put substrate-version "$version"
ok "ate.dev/substrate-version=$version"

step "Deploy ate-system"
if [[ -n "${SUBSTRATE_IMAGE_REPO:-}" ]]; then
  (cd "$SRC" && go run ./cmd/ate-setup deploy ate-system --image-repo "$SUBSTRATE_IMAGE_REPO" --image-tag "$SUBSTRATE_IMAGE_TAG")
  worker="$(cd "$SRC" && go run github.com/google/go-containerregistry/cmd/crane@latest digest "$SUBSTRATE_IMAGE_REPO/ateom-gvisor:$SUBSTRATE_IMAGE_TAG")"
  worker="$SUBSTRATE_IMAGE_REPO/ateom-gvisor:$SUBSTRATE_IMAGE_TAG@$worker"
else
  (cd "$SRC" && go run ./cmd/ate-setup deploy ate-system)
  ldflags=(); while IFS= read -r l; do [[ -n "$l" ]] && ldflags+=("--ldflags=$l"); done < <(cd "$SRC" && make -s ldflags)
  worker="$(cd "$SRC" && ./hack/run-tool.sh ko build --base-import-paths "${ldflags[@]}" ./cmd/ateom-gvisor)"
fi
[[ "$worker" == *@sha256:* ]] || die "worker image is not digest-pinned: $worker"
state_put worker-image "$worker"
ok "worker image $worker"

step "Label nodes"
# Every node, with --overwrite: after a SUBSTRATE_REF change the old version label must be replaced.
kubectl label nodes --all "ate.dev/substrate-version=$version" --overwrite >/dev/null
ok "nodes labeled"

"$REPO_ROOT/scripts/patch-router.sh"

step "Wait for ate-system"
kubectl -n ate-system wait --for=condition=Available deploy --all --timeout=10m
kubectl -n ate-system rollout status ds --timeout=10m
ok "Agent Substrate is ready"
kubectl -n ate-system get pods

# Written last, after node labeling and the router patch, so catch-up.sh can gate a skip on
# this single marker instead of re-deriving "is the install actually done" from several checks.
state_put install-complete "$(date -u +%Y-%m-%dT%H:%M:%SZ) SUBSTRATE_REF=$SUBSTRATE_REF"
