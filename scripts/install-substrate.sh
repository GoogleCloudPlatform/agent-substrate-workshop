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
(cd "$SRC" && go build -ldflags "-X=go.ate.dev/substrate/internal/version.Version=$ver" -o "$REPO_ROOT/bin/kubectl-ate" ./cmd/kubectl-ate)
ok "bin/kubectl-ate (kubectl ate …)"
user_has_ate || path_hint

step "Deploy ate-system"
# Clusters upgraded from Substrate < v0.6.0 may have a version-suffixed atelet DaemonSet
# (e.g. atelet-v0-4-0) holding hostPort 8085 and an incompatible bundled Postgres PVC
# (pre-v0.4.0 'postgres' schema owner, pre-v0.5.0 password-auth / enum wire types, or
# pre-v0.6.0 ActorTemplate resource limits / sandboxConfig wire types).
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
if kubectl -n ate-system get configmap ate-api-authentication -o jsonpath='{.data.authentication\.yaml}' 2>/dev/null | grep -q 'actorIdentityJWTProvider'; then
  warn "ate-api-authentication ConfigMap predates v0.6.0; deleting so ate-setup recreates it"
  kubectl -n ate-system delete configmap/ate-api-authentication --ignore-not-found
  kubectl -n ate-system delete pod -l app=ate-api-server --ignore-not-found >/dev/null
fi
if kubectl -n ate-system get sts postgres >/dev/null 2>&1; then
  pg_vols="$(kubectl -n ate-system get sts postgres -o jsonpath='{.spec.template.spec.volumes[*].name}' 2>/dev/null || true)"
  if [[ " $pg_vols " != *" postgres-ca "* ]] || ! kubectl -n ate-system get deploy ate-idp-server >/dev/null 2>&1; then
    warn "bundled Postgres predates v0.6.0; resetting sts/postgres and pvc/data-postgres-0"
    kubectl -n ate-system delete sts/postgres pvc/data-postgres-0 --ignore-not-found --wait=true
  fi
fi
if kubectl -n ate-system get ds atelet >/dev/null 2>&1; then
  atelet_inits="$(kubectl -n ate-system get ds atelet -o jsonpath='{.spec.template.spec.initContainers[*].name}' 2>/dev/null || true)"
  atelet_ready="$(kubectl -n ate-system get ds atelet -o jsonpath='{.status.numberReady}/{.status.desiredNumberScheduled}' 2>/dev/null || true)"
  if [[ " $atelet_inits " != *" objectstore-plugin "* ]] || [[ "$atelet_ready" != "${atelet_ready%/*}/${atelet_ready%/*}" ]]; then
    warn "clearing pre-v0.6.0 /var/lib/ate/image-cache (layout version 1 -> 2) on cluster nodes"
    kubectl -n ate-system delete ds/atelet --ignore-not-found --wait=true >/dev/null
    kubectl apply -f - >/dev/null <<'EOF'
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: atelet-cache-reset
  namespace: ate-system
spec:
  selector:
    matchLabels: {app: atelet-cache-reset}
  template:
    metadata:
      labels: {app: atelet-cache-reset}
    spec:
      tolerations:
      - {key: ate.dev/sandboxClass, operator: Exists, effect: NoSchedule}
      - {key: cloud.google.com/compute-class, operator: Exists, effect: NoSchedule}
      initContainers:
      - name: reset
        image: postgres:16
        command: ["sh", "-c", "if [ \"$(cat /var/lib/ate/image-cache/version 2>/dev/null)\" = 1 ]; then rm -rf /var/lib/ate/image-cache; fi"]
        securityContext: {privileged: true, runAsUser: 0}
        volumeMounts:
        - {name: ate-base, mountPath: /var/lib/ate}
      containers:
      - name: pause
        image: postgres:16
        command: ["sh", "-c", "sleep 3600"]
      volumes:
      - name: ate-base
        hostPath: {path: /var/lib/ate, type: DirectoryOrCreate}
EOF
    kubectl -n ate-system rollout status ds/atelet-cache-reset --timeout=180s >/dev/null
    kubectl -n ate-system delete ds/atelet-cache-reset --ignore-not-found --wait=true >/dev/null
  fi
fi
# v0.4.0+ requires an explicit --credential-provider selection on deploy ate-system / deploy atenet.
# {"name":"k8s.io"} deploys the bundled Kubernetes Secrets provider (with a default-deny namespace policy)
# so optional lab J works without reinstalling Substrate.
# v0.6.0+ also requires --snapshot-location, --snapshot-transfer-gsa-name, and --ingress-allowed-spiffe-ids.
CREDENTIAL_PROVIDER="${ATE_CREDENTIAL_PROVIDER:-{\"name\":\"k8s.io\"}}"
SNAPSHOT_LOCATION="${SNAPSHOT_LOCATION:-gs://${BUCKET_NAME}/}"
SNAPSHOT_TRANSFER_GSA_NAME="${SNAPSHOT_TRANSFER_GSA_NAME:-ate-snapshot-transfer}"
INGRESS_ALLOWED_SPIFFE_IDS="${ATE_INGRESS_ALLOWED_SPIFFE_IDS:-spiffe://cluster.local/ns/${NAMESPACE}/sa/gateway,spiffe://cluster.local/ns/ate-system/sa/atenet-router,spiffe://cluster.local/ns/ate-env/sa/ate-env-api,spiffe://cluster.local/ns/ax-system/sa/ax-server}"
export SNAPSHOT_LOCATION SNAPSHOT_TRANSFER_GSA_NAME ATE_INGRESS_ALLOWED_SPIFFE_IDS="$INGRESS_ALLOWED_SPIFFE_IDS"
if [[ -n "${SUBSTRATE_IMAGE_REPO:-}" ]]; then
  (cd "$SRC" && go run ./cmd/ate-setup deploy ate-system \
    --credential-provider="$CREDENTIAL_PROVIDER" \
    --snapshot-location="$SNAPSHOT_LOCATION" \
    --snapshot-transfer-gsa-name="$SNAPSHOT_TRANSFER_GSA_NAME" \
    --ingress-allowed-spiffe-ids="$INGRESS_ALLOWED_SPIFFE_IDS" \
    --image-repo "$SUBSTRATE_IMAGE_REPO" --image-tag "$SUBSTRATE_IMAGE_TAG")
  worker="$(cd "$SRC" && go run github.com/google/go-containerregistry/cmd/crane@latest digest "$SUBSTRATE_IMAGE_REPO/ateworker-gvisor:$SUBSTRATE_IMAGE_TAG")"
  worker="$SUBSTRATE_IMAGE_REPO/ateworker-gvisor:$SUBSTRATE_IMAGE_TAG@$worker"
else
  (cd "$SRC" && go run ./cmd/ate-setup deploy ate-system \
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
# If SUBSTRATE_REF changed since the last install, clear cached app images so catch-up rebuilds
# the gateway and agent against the current ateapipb wire schema.
if [[ "$(state_get install-complete)" != *"SUBSTRATE_REF=$SUBSTRATE_REF" ]]; then
  rm -f "$STATE_DIR/gateway-image" "$STATE_DIR/agent-image"
fi
state_put install-complete "$(date -u +%Y-%m-%dT%H:%M:%SZ) SUBSTRATE_REF=$SUBSTRATE_REF"
