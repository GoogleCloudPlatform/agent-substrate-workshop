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

# Full environment setup script for running the workshop in your own Google Cloud project.
# Automates end-to-end provisioning idempotently:
#   1. Initialize workshop.env from workshop.env.example (if missing or blank)
#   2. Ensure `ko` v0.19.1 is installed (auto-installs into bin/ko if missing on PATH)
#   3. Create (or reuse) the uniform-CPU GKE 1.37+ cluster + 48h upgrade exclusion + kubeconfig
#   4. Run preflight checks (`scripts/preflight.sh`)
#   5. Enable APIs, create GCS bucket, Artifact Registry, transfer GSA, IAM bindings,
#      ComputeClass, regional Gateways/GCPAgentGateway, and ReasoningEngine (`scripts/gcp-resources.sh`)
#   6. Install Agent Substrate v1.0.0, build bin/kubectl-ate, and patch atenet-router (`scripts/install-substrate.sh`)
#   7. Pre-pull the digest-pinned ateworker-gvisor image onto Ready cluster nodes (best-effort)
#   8. Build local CLI tools (`bin/ledger-verify`, `bin/evalrunner`)
#   9. Build & deploy the live degraded `prod-sim` environment (`ledger-api` + `ops-hub`)
#  10. Pre-build the `sre-gateway` image via ko so Module 01 deploys in seconds
#  11. Run Module 00 verification (`scripts/verify/00.sh`)
#
# Optional overrides:
#   PROJECT_ID=... REGION=us-central1 CLUSTER_LOCATION=us-central1-a CLUSTER_NAME=ate-cluster
#   CLUSTER_MACHINE_TYPE=c3-standard-4 CLUSTER_MIN_CPU_PLATFORM="Intel Skylake"
#   SKIP_CLUSTER_CREATE=1  Skip GKE cluster creation (use an existing pre-provisioned cluster)
#   SKIP_PREPULL=1         Skip pre-pulling ateworker-gvisor onto nodes
#   SKIP_GATEWAY_PREBUILD=1 Skip pre-building the sre-gateway image
#   SKIP_VERIFY=1          Skip scripts/verify/00.sh at the end
#   FORCE_REBUILD=1        Rebuild prod-sim and sre-gateway images even if cached state exists
source "$(dirname "$0")/lib.sh"

ENV_FILE="${WORKSHOP_ENV_FILE:-$REPO_ROOT/workshop.env}"
KO_VERSION="${KO_VERSION:-v0.19.1}"

derive_region_from_location() {
  local loc="${1:-}"
  if [[ "$loc" =~ ^([a-z]+-[a-z0-9]+)-[a-z]$ ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}"
  elif [[ "$loc" =~ ^[a-z]+-[a-z]+[0-9]+$ ]]; then
    printf '%s\n' "$loc"
  fi
}

init_workshop_env() {
  local proj="${PROJECT_ID:-}" region="${REGION:-}" loc="${CLUSTER_LOCATION:-}" cname="${CLUSTER_NAME:-}"
  if [[ -z "$region" && -n "$loc" ]]; then
    region="$(derive_region_from_location "$loc")"
  fi
  if [[ ! -f "$ENV_FILE" ]]; then
    if [[ -z "$proj" ]] && command -v gcloud >/dev/null 2>&1; then
      proj="$(gcloud config get-value project 2>/dev/null || true)"
      [[ "$proj" == "(unset)" ]] && proj=""
    fi
    [[ -n "$proj" ]] || die "PROJECT_ID is not set and no default gcloud project is configured; run: export PROJECT_ID=<your-project-id> && make setup"
    region="${region:-us-central1}"
    loc="${loc:-${region}-a}"
    cname="${cname:-ate-cluster}"

    step "Initialize ${ENV_FILE} from workshop.env.example"
    python3 - "$REPO_ROOT/workshop.env.example" "$ENV_FILE" "$proj" "$region" "$cname" "$loc" <<'PY'
import re, sys
src, dst, proj, region, cname, loc = sys.argv[1:7]
with open(src, encoding="utf-8") as fh:
    text = fh.read()
repls = {
    "PROJECT_ID": proj,
    "REGION": region,
    "CLUSTER_NAME": cname,
    "CLUSTER_LOCATION": loc,
}
for k, v in repls.items():
    pat = rf"^{k}=.*$"
    if re.search(pat, text, flags=re.MULTILINE):
        text = re.sub(pat, lambda _, k=k, v=v: f"{k}={v}", text, count=1, flags=re.MULTILINE)
    else:
        if text and not text.endswith("\n"):
            text += "\n"
        text += f"{k}={v}\n"
with open(dst, "w", encoding="utf-8") as fh:
    fh.write(text)
PY
    ok "wrote ${ENV_FILE} (PROJECT_ID=${proj}, REGION=${region}, CLUSTER_NAME=${cname}, CLUSTER_LOCATION=${loc})"
    return 0
  fi

  # If workshop.env already exists but PROJECT_ID, CLUSTER_LOCATION, or REGION is missing/blank,
  # populate or append missing keys from shell env or gcloud defaults.
  local file_proj file_loc file_reg
  file_proj="$(sed -n 's/^PROJECT_ID=//p' "$ENV_FILE" | head -n1)"
  file_loc="$(sed -n 's/^CLUSTER_LOCATION=//p' "$ENV_FILE" | head -n1)"
  file_reg="$(sed -n 's/^REGION=//p' "$ENV_FILE" | head -n1)"
  if [[ -z "$file_proj" || -z "$file_loc" || -z "$file_reg" ]]; then
    if [[ -z "$file_proj" ]]; then
      if [[ -z "$proj" ]] && command -v gcloud >/dev/null 2>&1; then
        proj="$(gcloud config get-value project 2>/dev/null || true)"
        [[ "$proj" == "(unset)" ]] && proj=""
      fi
      file_proj="$proj"
    fi
    if [[ -z "$file_loc" ]]; then
      local effective_reg="${region:-${file_reg:-us-central1}}"
      file_loc="${loc:-${effective_reg}-a}"
    fi
    if [[ -z "$region" && -z "$file_reg" ]]; then
      region="$(derive_region_from_location "$file_loc")"
      region="${region:-us-central1}"
    fi
    local updated
    updated="$(python3 - "$ENV_FILE" "$file_proj" "$file_loc" "${region}" "${cname}" <<'PY'
import re, sys
path, proj, loc, region, cname = sys.argv[1:6]
with open(path, encoding="utf-8") as fh:
    orig = fh.read()
text = orig

def upsert(content: str, key: str, val: str, only_if_blank: bool) -> str:
    if not val:
        return content
    present = re.search(rf"^{key}=(.*)$", content, flags=re.MULTILINE)
    if present:
        if only_if_blank and present.group(1).strip():
            return content
        return re.sub(rf"^{key}=.*$", lambda _, k=key, v=val: f"{k}={v}", content, count=1, flags=re.MULTILINE)
    if content and not content.endswith("\n"):
        content += "\n"
    return content + f"{key}={val}\n"

text = upsert(text, "PROJECT_ID", proj, only_if_blank=True)
text = upsert(text, "CLUSTER_LOCATION", loc, only_if_blank=True)
text = upsert(text, "REGION", region, only_if_blank=False)
text = upsert(text, "CLUSTER_NAME", cname, only_if_blank=False)

if text != orig:
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)
    print("changed")
PY
)"
    if [[ "$updated" == "changed" ]]; then
      step "Populate missing or blank fields in ${ENV_FILE}"
      ok "updated ${ENV_FILE} (PROJECT_ID=${file_proj}, REGION=${region:-${file_reg}}, CLUSTER_LOCATION=${file_loc})"
    fi
  fi
}

ensure_ko() {
  if command -v ko >/dev/null 2>&1; then
    ok "ko found at $(command -v ko)"
    return 0
  fi
  step "Install ko ${KO_VERSION} into bin/ko"
  mkdir -p "$REPO_ROOT/bin"
  if [[ "${CLOUD_SHELL:-}" == true ]]; then
    local tmp_gocache
    tmp_gocache="$(mktemp -d "${TMPDIR:-/tmp}/workshop-gocache.XXXXXX")"
    GOCACHE="$tmp_gocache" GOBIN="$REPO_ROOT/bin" go install "github.com/google/ko@${KO_VERSION}"
    rm -rf "$tmp_gocache"
  else
    GOBIN="$REPO_ROOT/bin" go install "github.com/google/ko@${KO_VERSION}"
  fi
  [[ -x "$REPO_ROOT/bin/ko" ]] || die "failed to install ko into $REPO_ROOT/bin/ko"
  ok "installed bin/ko (${KO_VERSION})"
}

PREPULL_PODS=()
_cleanup_prepull_pods() {
  local p
  (( ${#PREPULL_PODS[@]} > 0 )) || return 0
  for p in "${PREPULL_PODS[@]}"; do
    kubectl -n kube-system delete pod "$p" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  done
}

prepull_worker_image() {
  local worker_image
  worker_image="$(state_get worker-image || true)"
  [[ "$worker_image" == *@sha256:* ]] || return 0

  local nodes=() n
  while IFS= read -r n; do
    [[ -n "$n" ]] && nodes+=("$n")
  done < <(kubectl get nodes -o json 2>/dev/null \
    | jq -r '.items[]? | select(any(.status.conditions[]?; .type == "Ready" and .status == "True")) | .metadata.name' 2>/dev/null || true)
  (( ${#nodes[@]} > 0 )) || return 0

  step "Pre-pull ${worker_image} onto ${#nodes[@]} Ready node(s)"
  PREPULL_PODS=()
  trap '_cleanup_prepull_pods' EXIT
  trap '_cleanup_prepull_pods; exit 130' INT
  trap '_cleanup_prepull_pods; exit 143' TERM

  local idx=0 node pod_name
  for node in "${nodes[@]}"; do
    pod_name="ate-prepull-${idx}"
    PREPULL_PODS+=("$pod_name")
    kubectl -n kube-system delete pod "$pod_name" --ignore-not-found >/dev/null 2>&1 || true
    kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${pod_name}
  namespace: kube-system
spec:
  nodeName: ${node}
  restartPolicy: Never
  containers:
  - name: prepull
    image: ${worker_image}
    imagePullPolicy: IfNotPresent
    args: ["--version"]
EOF
    idx=$((idx + 1))
  done

  local timeout="${PREPULL_TIMEOUT:-300}"
  local poll_interval="${PREPULL_POLL_INTERVAL:-2}"
  local deadline=$((SECONDS + timeout))
  local pending=("${PREPULL_PODS[@]}")
  while (( ${#pending[@]} > 0 && SECONDS < deadline )); do
    local next_pending=() phase
    for pod_name in "${pending[@]}"; do
      phase="$(kubectl -n kube-system get pod "$pod_name" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
      case "$phase" in
        Succeeded)
          kubectl -n kube-system delete pod "$pod_name" --ignore-not-found --wait=false >/dev/null 2>&1 || true
          ;;
        Failed)
          warn "pre-pull pod ${pod_name} failed (WorkerPool will pull on startup)"
          kubectl -n kube-system delete pod "$pod_name" --ignore-not-found --wait=false >/dev/null 2>&1 || true
          ;;
        *)
          next_pending+=("$pod_name")
          ;;
      esac
    done
    if (( ${#next_pending[@]} > 0 )); then
      pending=("${next_pending[@]}")
      sleep "$poll_interval"
    else
      pending=()
    fi
  done

  if (( ${#pending[@]} > 0 )); then
    for pod_name in "${pending[@]}"; do
      warn "pre-pull pod ${pod_name} did not reach Succeeded within ${timeout}s (WorkerPool will pull on startup)"
    done
  fi
  _cleanup_prepull_pods
  PREPULL_PODS=()
  trap - EXIT INT TERM
  ok "ateworker-gvisor pre-pull complete"
}

wait_reasoning_engine() {
  local pid_file="$STATE_DIR/reasoning-engine.pid"
  [[ -f "$pid_file" ]] || return 0
  local pid
  pid="$(cat "$pid_file" 2>/dev/null || true)"
  [[ -n "$pid" ]] || return 0
  if kill -0 "$pid" 2>/dev/null; then
    step "Wait briefly for background ReasoningEngine discovery (up to 60s)"
    local waited=0
    while kill -0 "$pid" 2>/dev/null && (( waited < 60 )); do
      sleep 2
      waited=$((waited + 2))
    done
  fi
  if [[ -s "$STATE_DIR/reasoning-engine-id" ]]; then
    ok "managed session/memory runtime ready: $(state_get reasoning-engine-id)"
  fi
}

step "Full workshop setup"
init_workshop_env
load_env
require_cluster_location
ensure_ko

if [[ "${SKIP_CLUSTER_CREATE:-0}" == "1" ]]; then
  ok "SKIP_CLUSTER_CREATE=1: reusing existing cluster ${CLUSTER_NAME} (${CLUSTER_LOCATION})"
  gcloud container clusters get-credentials "$CLUSTER_NAME" --location "$CLUSTER_LOCATION" --project "$PROJECT_ID" >/dev/null 2>&1 || true
else
  "$REPO_ROOT/scripts/create-cluster.sh"
fi

"$REPO_ROOT/scripts/preflight.sh"
"$REPO_ROOT/scripts/gcp-resources.sh"
"$REPO_ROOT/scripts/install-substrate.sh"

if [[ "${SKIP_PREPULL:-0}" != "1" ]]; then
  prepull_worker_image
fi

step "Build local helper binaries (bin/ledger-verify, bin/evalrunner)"
make -s -C "$REPO_ROOT" tools
ok "local helper binaries built"

if [[ "${FORCE_REBUILD:-0}" == "1" ]] ||
   [[ ! -s "$STATE_DIR/ledger-api-image" || ! -s "$STATE_DIR/ops-hub-image" ]] ||
   ! kubectl -n "$SIM_NAMESPACE" get deploy ops-hub ledger-api >/dev/null 2>&1; then
  "$REPO_ROOT/scripts/prod-sim.sh"
else
  ok "prod-sim already deployed in ${SIM_NAMESPACE} (set FORCE_REBUILD=1 to rebuild)"
fi

if [[ "${SKIP_GATEWAY_PREBUILD:-0}" != "1" ]]; then
  if [[ "${FORCE_REBUILD:-0}" == "1" || ! -s "$STATE_DIR/gateway-image" ]]; then
    step "Pre-build Governed Broker (sre-gateway) image via ko"
    "$REPO_ROOT/scripts/build.sh" sre-gateway
    ok "sre-gateway image cached: $(state_get gateway-image)"
  else
    ok "sre-gateway image already cached: $(state_get gateway-image)"
  fi
fi

wait_reasoning_engine
state_put install-complete "$(date -u +%Y-%m-%dT%H:%M:%SZ) SUBSTRATE_REF=$SUBSTRATE_REF PROD_SIM=ready"

if [[ "${SKIP_VERIFY:-0}" != "1" ]]; then
  [[ -x "$REPO_ROOT/bin/kubectl-ate" ]] || die "missing executable $REPO_ROOT/bin/kubectl-ate after install"
  WORKSHOP_USER_PATH="$REPO_ROOT/bin:${WORKSHOP_USER_PATH}" "$REPO_ROOT/scripts/verify/00.sh"
fi

ok "Full workshop setup complete."
echo
echo "Load bin/ and workshop.env into your current shell before starting Module 01:"
echo "  export PATH=\"\$PWD/bin:\$PATH\""
echo "  source scripts/env.sh"
