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

# Installs Agent Substrate v1.0.0, builds bin/kubectl-ate, records the digest-pinned
# worker image, and applies the non-destructive router configuration via patch-router.sh.
source "$(dirname "$0")/lib.sh"
load_env
require_cluster_location

SRC="$STATE_DIR/substrate"
export KO_DOCKER_REPO KO_DEFAULTPLATFORMS
export GOWORK=off

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

step "Build bin/kubectl-ate"
mkdir -p "$REPO_ROOT/bin"
ver="$(git -C "$SRC" describe --tags --always --dirty)"
(cd "$SRC" && go build -ldflags "-X=go.ate.dev/substrate/internal/version.Version=$ver" -o "$REPO_ROOT/bin/kubectl-ate" ./cmd/kubectl-ate)
ok "bin/kubectl-ate"
user_has_ate || path_hint

step "Deploy ate-system"
CREDENTIAL_PROVIDER="${ATE_CREDENTIAL_PROVIDER:-{\"name\":\"k8s.io\"}}"
SNAPSHOT_LOCATION="${SNAPSHOT_LOCATION:-gs://${BUCKET_NAME}/}"
SNAPSHOT_TRANSFER_GSA_NAME="${SNAPSHOT_TRANSFER_GSA_NAME:-ate-snapshot-transfer}"
SANDBOX_CLASSES="${ATE_INSTALL_SANDBOX_CLASSES:-gvisor}"

# Preserve any SPIFFE IDs already configured on an existing shared cluster.
DEFAULT_SPIFFE_IDS="spiffe://cluster.local/ns/${NAMESPACE}/sa/sre-gateway,spiffe://cluster.local/ns/ate-system/sa/atenet-router,spiffe://cluster.local/ns/workshop/sa/gateway"
BASE_SPIFFE_IDS="${ATE_INGRESS_ALLOWED_SPIFFE_IDS:-$DEFAULT_SPIFFE_IDS}"
existing_spiffe="$(kubectl -n ate-system get deploy atenet-router -o json 2>/dev/null \
  | jq -r '(.spec.template.spec.containers[0].args // [])[] | select(startswith("--ingress-allowed-spiffe-ids=")) | sub("^--ingress-allowed-spiffe-ids="; "")' | head -n1 || true)"
if [[ -n "$existing_spiffe" ]]; then
  INGRESS_ALLOWED_SPIFFE_IDS="$(printf '%s,%s' "$existing_spiffe" "$BASE_SPIFFE_IDS" | tr ',' '\n' | awk 'NF && !seen[$0]++' | paste -sd',' -)"
else
  INGRESS_ALLOWED_SPIFFE_IDS="$BASE_SPIFFE_IDS"
fi
export SNAPSHOT_LOCATION SNAPSHOT_TRANSFER_GSA_NAME ATE_INGRESS_ALLOWED_SPIFFE_IDS="$INGRESS_ALLOWED_SPIFFE_IDS" ATE_INSTALL_SANDBOX_CLASSES="$SANDBOX_CLASSES"

ate_system_healthy_at_tag() {
  local tag="$1" api_json rtr_json ds_json
  api_json="$(kubectl -n ate-system get deploy ate-api-server -o json 2>/dev/null || true)"
  rtr_json="$(kubectl -n ate-system get deploy atenet-router -o json 2>/dev/null || true)"
  ds_json="$(kubectl -n ate-system get ds atelet -o json 2>/dev/null || true)"
  [[ -n "$api_json" && -n "$rtr_json" && -n "$ds_json" ]] || return 1
  jq -e --arg t "$tag" '
    (.spec.template.spec.containers[0].image // "" | (contains(":" + $t + "@") or endswith(":" + $t))) and
    (([.status.conditions[]? | select(.type == "Available") | .status][0] // "") == "True")
  ' <<<"$api_json" >/dev/null 2>&1 || return 1
  jq -e --arg t "$tag" '
    (.spec.template.spec.containers[0].image // "" | (contains(":" + $t + "@") or endswith(":" + $t))) and
    (([.status.conditions[]? | select(.type == "Available") | .status][0] // "") == "True")
  ' <<<"$rtr_json" >/dev/null 2>&1 || return 1
  jq -e --arg t "$tag" '
    (.spec.template.spec.containers[0].image // "" | (contains(":" + $t + "@") or endswith(":" + $t))) and
    ((.status.desiredNumberScheduled // 0) > 0) and
    ((.status.numberReady // 0) >= (.status.desiredNumberScheduled // 0))
  ' <<<"$ds_json" >/dev/null 2>&1
}

resolve_worker_image() {
  if [[ -n "${SUBSTRATE_IMAGE_REPO:-}" ]]; then
    local existing_worker digest
    existing_worker="$(kubectl get workerpools.ate.dev -A -o json 2>/dev/null \
      | jq -r --arg prefix "${SUBSTRATE_IMAGE_REPO}/ateworker-gvisor:${SUBSTRATE_IMAGE_TAG}@sha256:" \
        '[.items[]?.spec.workerImage // empty | select(startswith($prefix))][0] // empty' || true)"
    if [[ "$existing_worker" == *@sha256:* ]]; then
      printf '%s\n' "$existing_worker"
      return 0
    fi
    digest="$(cd "$SRC" && go run github.com/google/go-containerregistry/cmd/crane@latest digest "$SUBSTRATE_IMAGE_REPO/ateworker-gvisor:$SUBSTRATE_IMAGE_TAG")"
    printf '%s\n' "$SUBSTRATE_IMAGE_REPO/ateworker-gvisor:$SUBSTRATE_IMAGE_TAG@$digest"
  else
    local existing_worker
    existing_worker="$(kubectl get workerpools.ate.dev -A -o json 2>/dev/null \
      | jq -r '[.items[]?.spec.workerImage // empty | select(contains("@sha256:"))][0] // empty' || true)"
    if [[ "${1:-}" == "reuse-existing" && "$existing_worker" == *@sha256:* ]]; then
      printf '%s\n' "$existing_worker"
      return 0
    fi
    (cd "$SRC" && ./hack/run-tool.sh ko build --base-import-paths "--ldflags=-X=go.ate.dev/substrate/internal/version.Version=$ver" ./cmd/ateworker-gvisor)
  fi
}

TARGET_SUBSTRATE_TAG="${SUBSTRATE_IMAGE_TAG:-$SUBSTRATE_REF}"
if [[ "${FORCE_REINSTALL_SUBSTRATE:-false}" != "true" ]] && ate_system_healthy_at_tag "$TARGET_SUBSTRATE_TAG"; then
  ok "ate-system already healthy at ${TARGET_SUBSTRATE_TAG}; preserving existing control plane and Postgres state"
  worker="$(resolve_worker_image reuse-existing)"
elif [[ -n "${SUBSTRATE_IMAGE_REPO:-}" ]]; then
  (cd "$SRC" && go run ./cmd/ate-setup deploy ate-system \
    --sandbox-classes="$SANDBOX_CLASSES" \
    --credential-provider="$CREDENTIAL_PROVIDER" \
    --snapshot-location="$SNAPSHOT_LOCATION" \
    --snapshot-transfer-gsa-name="$SNAPSHOT_TRANSFER_GSA_NAME" \
    --ingress-allowed-spiffe-ids="$INGRESS_ALLOWED_SPIFFE_IDS" \
    --image-repo "$SUBSTRATE_IMAGE_REPO" --image-tag "$SUBSTRATE_IMAGE_TAG")
  worker="$(resolve_worker_image)"
else
  (cd "$SRC" && go run ./cmd/ate-setup deploy ate-system \
    --sandbox-classes="$SANDBOX_CLASSES" \
    --credential-provider="$CREDENTIAL_PROVIDER" \
    --snapshot-location="$SNAPSHOT_LOCATION" \
    --snapshot-transfer-gsa-name="$SNAPSHOT_TRANSFER_GSA_NAME" \
    --ingress-allowed-spiffe-ids="$INGRESS_ALLOWED_SPIFFE_IDS")
  worker="$(resolve_worker_image)"
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

if [[ " $(state_get install-complete || true) " != *" SUBSTRATE_REF=${SUBSTRATE_REF} "* ]]; then
  rm -f "$STATE_DIR/gateway-image" "$STATE_DIR/agent-image"
fi
state_put install-complete "$(date -u +%Y-%m-%dT%H:%M:%SZ) SUBSTRATE_REF=$SUBSTRATE_REF"
