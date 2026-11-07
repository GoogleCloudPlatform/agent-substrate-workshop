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
# Cloud Shell's home disk is 5 GB, and Go's build cache for substrate's tools takes a big share of it. There, build
# with a throwaway cache under /tmp (the VM's larger, ephemeral disk), removed when this script exits.
if [[ "${CLOUD_SHELL:-}" == true ]]; then
  GOCACHE="$(mktemp -d "${TMPDIR:-/tmp}/workshop-gocache.XXXXXX")"; export GOCACHE
  trap 'rm -rf "$GOCACHE"' EXIT
fi

step "Substrate checkout @ ${SUBSTRATE_REF}"
if [[ ! -d "$SRC/.git" ]]; then
  git clone --quiet https://github.com/agent-substrate/substrate "$SRC"
fi
git -C "$SRC" fetch --quiet --tags origin
git -C "$SRC" checkout --quiet "$SUBSTRATE_REF"
ok "$(git -C "$SRC" describe --tags --always)"

step "kubectl-ate"
mkdir -p "$REPO_ROOT/bin"
ver="$(git -C "$SRC" describe --tags --always --dirty)"
(cd "$SRC" && go build -ldflags "-X=go.ate.dev/substrate/internal/version.Version=$ver" -o "$REPO_ROOT/bin/kubectl-ate" ./cmd/kubectl-ate)
ok "bin/kubectl-ate (kubectl ate …)"
user_has_ate || path_hint

step "Deploy ate-system"
# {"name":"k8s.io"} deploys the bundled Kubernetes Secrets provider (with a default-deny namespace policy)
# so optional lab J works without reinstalling Substrate.
# Pass gvisor by default so make install doesn't require a separate -assets bucket or stage
# Kata/Cloud Hypervisor assets until optional lab I.
CREDENTIAL_PROVIDER="${ATE_CREDENTIAL_PROVIDER:-{\"name\":\"k8s.io\"}}"
SNAPSHOT_LOCATION="${SNAPSHOT_LOCATION:-gs://${BUCKET_NAME}/}"
SNAPSHOT_TRANSFER_GSA_NAME="${SNAPSHOT_TRANSFER_GSA_NAME:-ate-snapshot-transfer}"
INGRESS_ALLOWED_SPIFFE_IDS="${ATE_INGRESS_ALLOWED_SPIFFE_IDS:-spiffe://cluster.local/ns/${NAMESPACE}/sa/gateway,spiffe://cluster.local/ns/ate-system/sa/atenet-router,spiffe://cluster.local/ns/ate-env/sa/ate-env-api,spiffe://cluster.local/ns/ax-system/sa/ax-server}"
SANDBOX_CLASSES="${ATE_INSTALL_SANDBOX_CLASSES:-gvisor}"
export SNAPSHOT_LOCATION SNAPSHOT_TRANSFER_GSA_NAME ATE_INGRESS_ALLOWED_SPIFFE_IDS="$INGRESS_ALLOWED_SPIFFE_IDS" ATE_INSTALL_SANDBOX_CLASSES="$SANDBOX_CLASSES"
if [[ -n "${SUBSTRATE_IMAGE_REPO:-}" ]]; then
  (cd "$SRC" && go run ./cmd/ate-setup deploy ate-system \
    --sandbox-classes="$SANDBOX_CLASSES" \
    --credential-provider="$CREDENTIAL_PROVIDER" \
    --snapshot-location="$SNAPSHOT_LOCATION" \
    --snapshot-transfer-gsa-name="$SNAPSHOT_TRANSFER_GSA_NAME" \
    --ingress-allowed-spiffe-ids="$INGRESS_ALLOWED_SPIFFE_IDS" \
    --image-repo "$SUBSTRATE_IMAGE_REPO" --image-tag "$SUBSTRATE_IMAGE_TAG")
  worker="$(cd "$SRC" && go run github.com/google/go-containerregistry/cmd/crane@latest digest "$SUBSTRATE_IMAGE_REPO/ateworker-gvisor:$SUBSTRATE_IMAGE_TAG")"
  worker="$SUBSTRATE_IMAGE_REPO/ateworker-gvisor:$SUBSTRATE_IMAGE_TAG@$worker"
else
  (cd "$SRC" && go run ./cmd/ate-setup deploy ate-system \
    --sandbox-classes="$SANDBOX_CLASSES" \
    --credential-provider="$CREDENTIAL_PROVIDER" \
    --snapshot-location="$SNAPSHOT_LOCATION" \
    --snapshot-transfer-gsa-name="$SNAPSHOT_TRANSFER_GSA_NAME" \
    --ingress-allowed-spiffe-ids="$INGRESS_ALLOWED_SPIFFE_IDS")
  worker="$(cd "$SRC" && ./hack/run-tool.sh ko build --base-import-paths "--ldflags=-X=go.ate.dev/substrate/internal/version.Version=$ver" ./cmd/ateworker-gvisor)"
fi
[[ "$worker" == *@sha256:* ]] || die "worker image is not digest-pinned: $worker"
state_put worker-image "$worker"
ok "worker image $worker"

"$REPO_ROOT/scripts/patch-router.sh"

step "Wait for ate-system"
kubectl -n ate-system wait --for=condition=Available deploy --all --timeout=10m
kubectl -n ate-system rollout status ds/atelet --timeout=10m
ok "Agent Substrate is ready"
kubectl -n ate-system get pods

# Written last, after the router patch, so catch-up.sh can gate a skip on
# this single marker instead of re-deriving "is the install actually done" from several checks.
state_put install-complete "$(date -u +%Y-%m-%dT%H:%M:%SZ) SUBSTRATE_REF=$SUBSTRATE_REF"
