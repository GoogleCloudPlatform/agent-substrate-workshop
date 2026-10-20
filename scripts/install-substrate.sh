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
ver="$(git -C "$SRC" describe --tags --always --dirty)"
(cd "$SRC" && go build -ldflags "-X=github.com/agent-substrate/substrate/internal/version.Version=$ver" -o "$REPO_ROOT/bin/kubectl-ate" ./cmd/kubectl-ate)
ok "bin/kubectl-ate (kubectl ate …)"
user_has_ate || path_hint

step "Deploy ate-system"
# Clusters upgraded from Substrate < v0.5.0 may have a version-suffixed atelet DaemonSet
# (e.g. atelet-v0-4-0) holding hostPort 8085 and an incompatible bundled Postgres PVC
# (pre-v0.4.0 'postgres' schema owner, or pre-v0.5.0 password-auth / enum wire types).
# Clean both up before ate-setup deploys and waits for ds/atelet and sts/postgres.
for old_ds in $(kubectl -n ate-system get ds -l app=atelet -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null); do
  if [[ "$old_ds" != "atelet" ]]; then
    warn "removing legacy DaemonSet $old_ds before deploying ds/atelet"
    kubectl -n ate-system delete ds "$old_ds" --ignore-not-found --wait=true >/dev/null
  fi
done
kubectl label nodes --all ate.dev/substrate-version- >/dev/null 2>&1 || true
if [[ -f "$SRC/manifests/ate-install/sandboxconfig-validation.yaml" ]]; then
  kubectl apply -f "$SRC/manifests/ate-install/generated/ate.dev_sandboxconfigs.yaml" \
    -f "$SRC/manifests/ate-install/sandboxconfig-validation.yaml" >/dev/null 2>&1 || true
fi
if kubectl -n ate-system get sts postgres >/dev/null 2>&1; then
  pg_vols="$(kubectl -n ate-system get sts postgres -o jsonpath='{.spec.template.spec.volumes[*].name}' 2>/dev/null || true)"
  if [[ " $pg_vols " != *" postgres-ca "* ]]; then
    warn "bundled Postgres predates v0.5.0; resetting sts/postgres and pvc/data-postgres-0"
    kubectl -n ate-system delete sts/postgres pvc/data-postgres-0 --ignore-not-found --wait=true
  fi
fi
# v0.4.0+ requires an explicit --credential-provider selection on deploy ate-system / deploy atenet.
# {"name":"k8s.io"} deploys the bundled Kubernetes Secrets provider (with a default-deny namespace policy)
# so optional lab J works without reinstalling Substrate.
CREDENTIAL_PROVIDER="${ATE_CREDENTIAL_PROVIDER:-{\"name\":\"k8s.io\"}}"
if [[ -n "${SUBSTRATE_IMAGE_REPO:-}" ]]; then
  (cd "$SRC" && go run ./cmd/ate-setup deploy ate-system --credential-provider="$CREDENTIAL_PROVIDER" --image-repo "$SUBSTRATE_IMAGE_REPO" --image-tag "$SUBSTRATE_IMAGE_TAG")
  worker="$(cd "$SRC" && go run github.com/google/go-containerregistry/cmd/crane@latest digest "$SUBSTRATE_IMAGE_REPO/ateom-gvisor:$SUBSTRATE_IMAGE_TAG")"
  worker="$SUBSTRATE_IMAGE_REPO/ateom-gvisor:$SUBSTRATE_IMAGE_TAG@$worker"
else
  (cd "$SRC" && go run ./cmd/ate-setup deploy ate-system --credential-provider="$CREDENTIAL_PROVIDER")
  worker="$(cd "$SRC" && ./hack/run-tool.sh ko build --base-import-paths "--ldflags=-X=github.com/agent-substrate/substrate/internal/version.Version=$ver" ./cmd/ateom-gvisor)"
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
