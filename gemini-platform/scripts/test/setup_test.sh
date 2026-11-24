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

# Offline behavioral tests for scripts/create-cluster.sh, scripts/setup.sh, and
# core API / Workload Identity bindings in scripts/gcp-resources.sh.
# shellcheck disable=SC2329,SC2030,SC2031 # t_* run via check(); per-test exports are subshell-local
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT

fails=0
check() { local name="$1"; shift; if "$@"; then echo "ok   $name"; else echo "FAIL $name"; fails=$((fails+1)); fi; }

t_create_cluster_fresh_default_c3() {
  local d; d="$(mktemp -d "$TMP/cc-fresh.XXXXXX")"
  mkdir -p "$d/bin" "$d/state"
  cat >"$d/env" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_NAME=ate-cluster
CLUSTER_LOCATION=us-central1-a
EOF
  cat >"$d/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "gcloud $*" >>"$CC_LOG"
case "$*" in
  "container clusters describe "*)
    if [[ -f "$CREATED_FLAG" ]]; then
      echo '{"nodePools":[{"management":{"autoUpgrade":true}}]}'
      exit 0
    fi
    exit 1 ;;
  "beta container clusters create "*)
    touch "$CREATED_FLAG"
    exit 0 ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/gcloud"

  : >"$d/cc.log"
  CC_LOG="$d/cc.log" CREATED_FLAG="$d/created" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/create-cluster.sh" >/dev/null 2>&1 || return 1

  grep -Fq "services enable --project demo-proj serviceusage.googleapis.com cloudresourcemanager.googleapis.com compute.googleapis.com container.googleapis.com monitoring.googleapis.com logging.googleapis.com cloudtrace.googleapis.com telemetry.googleapis.com" "$d/cc.log" &&
    grep -Fq "beta container clusters create ate-cluster" "$d/cc.log" &&
    grep -Fq -- "--machine-type c3-standard-4" "$d/cc.log" &&
    grep -Fq -- "--disk-type pd-balanced --disk-size 100" "$d/cc.log" &&
    grep -Fq -- "--gateway-api=standard" "$d/cc.log" &&
    grep -Fq -- "--workload-pool=demo-proj.svc.id.goog" "$d/cc.log" &&
    grep -Fq -- "--enable-managed-prometheus" "$d/cc.log" &&
    grep -Fq -- "--managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS" "$d/cc.log" &&
    grep -Fq "container clusters update ate-cluster" "$d/cc.log" &&
    grep -Fq -- "--add-maintenance-exclusion-scope no_minor_or_node_upgrades" "$d/cc.log" &&
    grep -Fq "container clusters get-credentials ate-cluster" "$d/cc.log"
}

t_create_cluster_hyperdisk_for_n4_c4_and_autopin_n2() {
  local d; d="$(mktemp -d "$TMP/cc-mach.XXXXXX")"
  mkdir -p "$d/bin" "$d/state"
  cat >"$d/env" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_NAME=ate-cluster
CLUSTER_LOCATION=us-central1-a
EOF
  cat >"$d/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "gcloud $*" >>"$CC_LOG"
case "$*" in
  "container clusters describe "*)
    if [[ -f "$CREATED_FLAG" ]]; then
      echo '{"nodePools":[{"management":{"autoUpgrade":false}}]}'
      exit 0
    fi
    exit 1 ;;
  "beta container clusters create "*)
    touch "$CREATED_FLAG"
    exit 0 ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/gcloud"

  # n4-* defaults to hyperdisk-balanced
  : >"$d/cc.log"; rm -f "$d/created"
  CLUSTER_MACHINE_TYPE=n4-standard-4 CC_LOG="$d/cc.log" CREATED_FLAG="$d/created" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/create-cluster.sh" >/dev/null 2>&1 || return 1
  grep -Fq -- "--machine-type n4-standard-4" "$d/cc.log" || return 1
  grep -Fq -- "--disk-type hyperdisk-balanced" "$d/cc.log" || return 1
  ! grep -Fq -- "--min-cpu-platform" "$d/cc.log" || return 1

  # c4-* defaults to hyperdisk-balanced, and explicit CLUSTER_DISK_TYPE overrides it
  : >"$d/cc.log"; rm -f "$d/created"
  CLUSTER_MACHINE_TYPE=c4-standard-4 CC_LOG="$d/cc.log" CREATED_FLAG="$d/created" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/create-cluster.sh" >/dev/null 2>&1 || return 1
  grep -Fq -- "--disk-type hyperdisk-balanced" "$d/cc.log" || return 1

  : >"$d/cc.log"; rm -f "$d/created"
  CLUSTER_MACHINE_TYPE=n4-standard-4 CLUSTER_DISK_TYPE=hyperdisk-extreme CC_LOG="$d/cc.log" CREATED_FLAG="$d/created" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/create-cluster.sh" >/dev/null 2>&1 || return 1
  grep -Fq -- "--disk-type hyperdisk-extreme" "$d/cc.log" || return 1

  # n2-* auto-pins Intel Ice Lake and keeps pd-balanced
  : >"$d/cc.log"; rm -f "$d/created"
  CLUSTER_MACHINE_TYPE=n2-standard-4 CC_LOG="$d/cc.log" CREATED_FLAG="$d/created" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/create-cluster.sh" >/dev/null 2>&1 || return 1
  grep -Fq -- "--machine-type n2-standard-4" "$d/cc.log" &&
    grep -Fq -- "--disk-type pd-balanced" "$d/cc.log" &&
    grep -Fq -- "--min-cpu-platform Intel Ice Lake" "$d/cc.log"
}

t_create_cluster_empty_nodepools_and_expired_exclusion() {
  local d; d="$(mktemp -d "$TMP/cc-exp.XXXXXX")"
  mkdir -p "$d/bin" "$d/state"
  cat >"$d/env" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_NAME=ate-cluster
CLUSTER_LOCATION=us-central1-a
EOF
  cat >"$d/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "gcloud $*" >>"$CC_LOG"
case "$*" in
  "container clusters describe "*)
    cat "$DESC_JSON"
    exit 0 ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/gcloud"

  # Empty nodePools must not skip adding workshop-freeze.
  printf '{"nodePools":[]}\n' >"$d/empty.json"
  : >"$d/cc.log"
  DESC_JSON="$d/empty.json" CC_LOG="$d/cc.log" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/create-cluster.sh" >/dev/null 2>&1 || return 1
  grep -Fq -- "--add-maintenance-exclusion-name workshop-freeze" "$d/cc.log" || return 1
  ! grep -Fq -- "--remove-maintenance-exclusion" "$d/cc.log" || return 1

  # Expired workshop-freeze must be removed before re-adding.
  cat >"$d/expired.json" <<'JSON'
{"nodePools":[{"management":{"autoUpgrade":true}}],"maintenancePolicy":{"window":{"maintenanceExclusions":{"workshop-freeze":{"startTime":"2000-01-01T00:00:00Z","endTime":"2000-02-01T00:00:00Z","maintenanceExclusionOptions":{"scope":"NO_MINOR_OR_NODE_UPGRADES"}}}}}}
JSON
  : >"$d/cc.log"
  DESC_JSON="$d/expired.json" CC_LOG="$d/cc.log" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/create-cluster.sh" >/dev/null 2>&1 || return 1
  local rm_line add_line
  rm_line="$(grep -n -- "--remove-maintenance-exclusion workshop-freeze" "$d/cc.log" | cut -d: -f1 | head -n1)"
  add_line="$(grep -n -- "--add-maintenance-exclusion-name workshop-freeze" "$d/cc.log" | cut -d: -f1 | head -n1)"
  [[ -n "$rm_line" && -n "$add_line" && "$rm_line" -lt "$add_line" ]]
}

t_create_cluster_auto_pins_skylake_for_n1() {
  local d; d="$(mktemp -d "$TMP/cc-n1.XXXXXX")"
  mkdir -p "$d/bin" "$d/state"
  cat >"$d/env" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_NAME=ate-cluster
CLUSTER_LOCATION=us-central1-a
EOF
  cat >"$d/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "gcloud $*" >>"$CC_LOG"
case "$*" in
  "container clusters describe "*)
    if [[ -f "$CREATED_FLAG" ]]; then
      echo '{"nodePools":[{"management":{"autoUpgrade":false}}]}'
      exit 0
    fi
    exit 1 ;;
  "beta container clusters create "*)
    touch "$CREATED_FLAG"
    exit 0 ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/gcloud"

  : >"$d/cc.log"
  CLUSTER_MACHINE_TYPE=n1-standard-4 CC_LOG="$d/cc.log" CREATED_FLAG="$d/created" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/create-cluster.sh" >/dev/null 2>&1 || return 1

  grep -Fq -- "--machine-type n1-standard-4" "$d/cc.log" &&
    grep -Fq -- "--min-cpu-platform Intel Skylake" "$d/cc.log" &&
    ! grep -Fq "container clusters update" "$d/cc.log"
}

t_create_cluster_idempotent_when_exists_with_active_exclusion() {
  local d; d="$(mktemp -d "$TMP/cc-exists.XXXXXX")"
  mkdir -p "$d/bin" "$d/state"
  cat >"$d/env" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_NAME=ate-cluster
CLUSTER_LOCATION=us-central1-a
EOF
  cat >"$d/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "gcloud $*" >>"$CC_LOG"
case "$*" in
  "container clusters describe "*)
    echo '{"nodePools":[{"management":{"autoUpgrade":true}}],"maintenancePolicy":{"window":{"maintenanceExclusions":{"workshop-freeze":{"startTime":"2000-01-01T00:00:00Z","endTime":"2999-01-01T00:00:00Z","maintenanceExclusionOptions":{"scope":"NO_MINOR_OR_NODE_UPGRADES"}}}}}}'
    exit 0 ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/gcloud"

  : >"$d/cc.log"
  CC_LOG="$d/cc.log" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/create-cluster.sh" >/dev/null 2>&1 || return 1

  ! grep -Fq "clusters create" "$d/cc.log" &&
    ! grep -Fq "clusters update" "$d/cc.log" &&
    grep -Fq "container clusters get-credentials ate-cluster" "$d/cc.log"
}

t_gcp_resources_core_apis_and_controller_wi() {
  local d; d="$(mktemp -d "$TMP/gcp-par.XXXXXX")"
  mkdir -p "$d/bin" "$d/state"
  cat >"$d/env" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_NAME=demo-cluster
CLUSTER_LOCATION=us-central1-a
BUCKET_NAME=${PROJECT_ID}-ate
NAMESPACE=workshop-301
ATESPACE=workshop-301
SIM_NAMESPACE=prod-sim
EOF
  cat >"$d/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "gcloud $*" >>"$GCP_LOG"
case "$*" in
  "projects describe demo-proj --format=value(projectNumber)") echo "11223344" ;;
  "projects get-ancestors demo-proj --format=json") echo '[]' ;;
  *) exit 0 ;;
esac
STUB
  cat >"$d/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
exit 1
STUB
  cat >"$d/bin/nohup" <<'STUB'
#!/usr/bin/env bash
exit 0
STUB
  chmod +x "$d/bin/gcloud" "$d/bin/kubectl" "$d/bin/nohup"

  : >"$d/gcp.log"
  GCP_LOG="$d/gcp.log" PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/gcp-resources.sh" >/dev/null 2>&1 || return 1

  local ctrl_member="principal://iam.googleapis.com/projects/11223344/locations/global/workloadIdentityPools/demo-proj.svc.id.goog/subject/ns/ate-system/sa/ate-snapshot-controller"
  grep -Fq "serviceusage.googleapis.com" "$d/gcp.log" &&
    grep -Fq "cloudresourcemanager.googleapis.com" "$d/gcp.log" &&
    grep -Fq "compute.googleapis.com" "$d/gcp.log" &&
    grep -Fq "iam service-accounts add-iam-policy-binding ate-snapshot-transfer@demo-proj.iam.gserviceaccount.com --project demo-proj --member=${ctrl_member} --role=roles/iam.workloadIdentityUser" "$d/gcp.log" &&
    grep -Fq "compute networks subnets create proxy-only-us-central1 --project demo-proj --purpose=REGIONAL_MANAGED_PROXY --role=ACTIVE --region=us-central1 --network=default --range=10.127.0.0/23" "$d/gcp.log" || return 1

  # When an ACTIVE REGIONAL_MANAGED_PROXY subnet already exists, skip creation.
  cat >"$d/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "gcloud $*" >>"$GCP_LOG"
case "$*" in
  "projects describe demo-proj --format=value(projectNumber)") echo "11223344" ;;
  "projects get-ancestors demo-proj --format=json") echo '[]' ;;
  "container clusters describe "*) echo "custom-vpc" ;;
  "compute networks subnets list "*) echo "existing-proxy-subnet" ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/gcloud"
  : >"$d/gcp.log"
  GCP_LOG="$d/gcp.log" PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/gcp-resources.sh" >/dev/null 2>&1 || return 1
  grep -Fq "compute networks subnets list --project demo-proj --network custom-vpc --regions us-central1" "$d/gcp.log" &&
    ! grep -Fq "compute networks subnets create" "$d/gcp.log"
}

t_setup_sh_orchestrates_full_bootstrap_and_idempotent_rerun() {
  local d; d="$(mktemp -d "$TMP/setup.XXXXXX")"
  mkdir -p "$d/scripts/verify" "$d/state" "$d/mockbin"
  cp "$ROOT/scripts/lib.sh" "$ROOT/scripts/setup.sh" "$d/scripts/"
  cp "$ROOT/workshop.env.example" "$d/workshop.env.example"
  chmod +x "$d/scripts/setup.sh"

  cat >"$d/scripts/create-cluster.sh" <<'STUB'
#!/usr/bin/env bash
echo "create-cluster" >>"$STEP_LOG"
STUB
  cat >"$d/scripts/preflight.sh" <<'STUB'
#!/usr/bin/env bash
echo "preflight" >>"$STEP_LOG"
STUB
  cat >"$d/scripts/gcp-resources.sh" <<'STUB'
#!/usr/bin/env bash
echo "gcp-resources" >>"$STEP_LOG"
STUB
  cat >"$d/scripts/install-substrate.sh" <<'STUB'
#!/usr/bin/env bash
echo "install-substrate" >>"$STEP_LOG"
mkdir -p "$WORKSHOP_ROOT/bin"
printf '#!/usr/bin/env bash\nexit 0\n' >"$WORKSHOP_ROOT/bin/kubectl-ate"
chmod +x "$WORKSHOP_ROOT/bin/kubectl-ate"
printf 'ghcr.io/agent-substrate/substrate/ateworker-gvisor:v1.0.0@sha256:abc123\n' >"$WORKSHOP_STATE_DIR/worker-image"
STUB
  cat >"$d/scripts/prod-sim.sh" <<'STUB'
#!/usr/bin/env bash
echo "prod-sim" >>"$STEP_LOG"
printf 'r/ledger@sha256:111\n' >"$WORKSHOP_STATE_DIR/ledger-api-image"
printf 'r/opshub@sha256:222\n' >"$WORKSHOP_STATE_DIR/ops-hub-image"
STUB
  cat >"$d/scripts/build.sh" <<'STUB'
#!/usr/bin/env bash
echo "build:$*" >>"$STEP_LOG"
printf 'r/gateway@sha256:333\n' >"$WORKSHOP_STATE_DIR/gateway-image"
STUB
  cat >"$d/scripts/verify/00.sh" <<'STUB'
#!/usr/bin/env bash
echo "verify00:upath=${WORKSHOP_USER_PATH}" >>"$STEP_LOG"
[[ "$WORKSHOP_USER_PATH" == *"$WORKSHOP_ROOT/bin"* ]] || exit 1
STUB
  chmod +x "$d/scripts/"*.sh "$d/scripts/verify/00.sh"

  cat >"$d/mockbin/gcloud" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  "config get-value project") echo "auto-proj" ;;
  *) exit 0 ;;
esac
STUB
  cat >"$d/mockbin/go" <<'STUB'
#!/usr/bin/env bash
echo "go:$*" >>"$STEP_LOG"
if [[ "$1" == "install" && -n "${GOBIN:-}" ]]; then
  mkdir -p "$GOBIN"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$GOBIN/ko"
  chmod +x "$GOBIN/ko"
fi
STUB
  cat >"$d/mockbin/make" <<'STUB'
#!/usr/bin/env bash
echo "make:$*" >>"$STEP_LOG"
STUB
  cat >"$d/mockbin/kubectl" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  "get nodes -o json")
    cat <<'JSON'
{"items":[
  {"metadata":{"name":"node-1"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},
  {"metadata":{"name":"node-not-ready"},"status":{"conditions":[{"type":"Ready","status":"False"}]}},
  {"metadata":{"name":"node-2"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}
]}
JSON
    ;;
  *"apply -f -"*)
    body="$(cat)"
    node="$(awk '/nodeName:/ {print $2}' <<<"$body")"
    echo "prepull-apply:${node}" >>"$STEP_LOG"
    ;;
  *"get pod ate-prepull-0 -o jsonpath="*) echo "Succeeded" ;;
  *"get pod ate-prepull-1 -o jsonpath="*) echo "Failed" ;;
  *"delete pod "*)
    echo "prepull-delete:$*" >>"$STEP_LOG"
    ;;
  *"get deploy ops-hub ledger-api"*) [[ -f "$WORKSHOP_STATE_DIR/ledger-api-image" ]] && exit 0 || exit 1 ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/mockbin/"*

  # Build a minimal PATH without ko so setup.sh triggers auto-install of bin/ko.
  local minimal_path="$d/mockbin:/usr/bin:/bin"
  : >"$d/steps.log"
  unset PROJECT_ID REGION CLUSTER_LOCATION CLUSTER_NAME WORKSHOP_USER_PATH || true
  PATH="$minimal_path" STEP_LOG="$d/steps.log" WORKSHOP_ROOT="$d" \
    PREPULL_POLL_INTERVAL=0 \
    WORKSHOP_ENV_FILE="$d/workshop.env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$d/scripts/setup.sh" >/dev/null 2>&1 || return 1

  [[ -f "$d/workshop.env" ]] || return 1
  grep -Fq "PROJECT_ID=auto-proj" "$d/workshop.env" || return 1
  grep -Fq "CLUSTER_LOCATION=us-central1-a" "$d/workshop.env" || return 1
  [[ -x "$d/bin/ko" ]] || return 1
  grep -Fq "go:install github.com/google/ko@v0.19.1" "$d/steps.log" || return 1
  grep -Fq "create-cluster" "$d/steps.log" || return 1
  grep -Fq "preflight" "$d/steps.log" || return 1
  grep -Fq "gcp-resources" "$d/steps.log" || return 1
  grep -Fq "install-substrate" "$d/steps.log" || return 1
  grep -Fq "prepull-apply:node-1" "$d/steps.log" || return 1
  grep -Fq "prepull-apply:node-2" "$d/steps.log" || return 1
  ! grep -Fq "prepull-apply:node-not-ready" "$d/steps.log" || return 1
  grep -Fq "prepull-delete:-n kube-system delete pod ate-prepull-0" "$d/steps.log" || return 1
  grep -Fq "prepull-delete:-n kube-system delete pod ate-prepull-1" "$d/steps.log" || return 1
  grep -Fq "prod-sim" "$d/steps.log" || return 1
  grep -Fq "build:sre-gateway" "$d/steps.log" || return 1
  grep -Fq "verify00:upath=$d/bin:" "$d/steps.log" || return 1
  grep -Fq "PROD_SIM=ready" "$d/state/install-complete" || return 1

  # Re-run with SKIP_CLUSTER_CREATE=1 SKIP_PREPULL=1: skips create-cluster, ko install, prepull, prod-sim, and gateway build.
  : >"$d/steps.log"
  PATH="$minimal_path" STEP_LOG="$d/steps.log" WORKSHOP_ROOT="$d" \
    SKIP_CLUSTER_CREATE=1 SKIP_PREPULL=1 \
    WORKSHOP_ENV_FILE="$d/workshop.env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$d/scripts/setup.sh" >/dev/null 2>&1 || return 1

  ! grep -Fq "create-cluster" "$d/steps.log" &&
    ! grep -Fq "go:install" "$d/steps.log" &&
    ! grep -Fq "prepull-apply" "$d/steps.log" &&
    ! grep -Fq "prod-sim" "$d/steps.log" &&
    ! grep -Fq "build:sre-gateway" "$d/steps.log" &&
    grep -Fq "verify00:" "$d/steps.log"
}

t_setup_sh_derives_region_from_cluster_location() {
  local d; d="$(mktemp -d "$TMP/setup-reg.XXXXXX")"
  mkdir -p "$d/scripts/verify" "$d/state" "$d/mockbin"
  cp "$ROOT/scripts/lib.sh" "$ROOT/scripts/setup.sh" "$d/scripts/"
  cp "$ROOT/workshop.env.example" "$d/workshop.env.example"
  chmod +x "$d/scripts/setup.sh"
  for s in create-cluster preflight gcp-resources install-substrate prod-sim build; do
    printf '#!/usr/bin/env bash\nexit 0\n' >"$d/scripts/${s}.sh"
    chmod +x "$d/scripts/${s}.sh"
  done
  printf '#!/usr/bin/env bash\nexit 0\n' >"$d/mockbin/ko"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$d/mockbin/make"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$d/mockbin/kubectl"
  cat >"$d/mockbin/gcloud" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  "config get-value project") echo "(unset)" ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/mockbin/"*

  PATH="$d/mockbin:/usr/bin:/bin" PROJECT_ID=eu-proj CLUSTER_LOCATION=europe-west1-b \
    SKIP_PREPULL=1 SKIP_GATEWAY_PREBUILD=1 SKIP_VERIFY=1 \
    WORKSHOP_ENV_FILE="$d/workshop.env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$d/scripts/setup.sh" >/dev/null 2>&1 || return 1

  grep -Fq "PROJECT_ID=eu-proj" "$d/workshop.env" &&
    grep -Fq "REGION=europe-west1" "$d/workshop.env" &&
    grep -Fq "CLUSTER_LOCATION=europe-west1-b" "$d/workshop.env" || return 1

  # Regional CLUSTER_LOCATION (e.g. europe-west1) also derives REGION=europe-west1.
  rm -f "$d/workshop.env"
  PATH="$d/mockbin:/usr/bin:/bin" PROJECT_ID=eu-proj CLUSTER_LOCATION=europe-west1 \
    SKIP_PREPULL=1 SKIP_GATEWAY_PREBUILD=1 SKIP_VERIFY=1 \
    WORKSHOP_ENV_FILE="$d/workshop.env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$d/scripts/setup.sh" >/dev/null 2>&1 || return 1

  grep -Fq "PROJECT_ID=eu-proj" "$d/workshop.env" &&
    grep -Fq "REGION=europe-west1" "$d/workshop.env" &&
    grep -Fq "CLUSTER_LOCATION=europe-west1" "$d/workshop.env" || return 1

  # Existing workshop.env missing PROJECT_ID= and CLUSTER_LOCATION= keys appends them.
  printf 'REGION=us-central1\n' >"$d/workshop.env"
  local out
  out="$(PATH="$d/mockbin:/usr/bin:/bin" PROJECT_ID=appended-proj CLUSTER_LOCATION=europe-west1 \
    SKIP_PREPULL=1 SKIP_GATEWAY_PREBUILD=1 SKIP_VERIFY=1 \
    WORKSHOP_ENV_FILE="$d/workshop.env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$d/scripts/setup.sh" 2>&1)" || return 1

  grep -Fq "PROJECT_ID=appended-proj" "$d/workshop.env" &&
    grep -Fq "CLUSTER_LOCATION=europe-west1" "$d/workshop.env" &&
    grep -Fq "REGION=europe-west1" "$d/workshop.env" &&
    [[ "$out" == *"updated $d/workshop.env"* ]] || return 1

  # Existing workshop.env with blank PROJECT_ID and no env/gcloud default does not claim updated.
  printf 'PROJECT_ID=\nCLUSTER_LOCATION=us-central1-a\nREGION=us-central1\n' >"$d/workshop.env"
  unset PROJECT_ID REGION CLUSTER_LOCATION CLUSTER_NAME || true
  out="$(PATH="$d/mockbin:/usr/bin:/bin" \
    SKIP_PREPULL=1 SKIP_GATEWAY_PREBUILD=1 SKIP_VERIFY=1 \
    WORKSHOP_ENV_FILE="$d/workshop.env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$d/scripts/setup.sh" 2>&1 || true)"
  [[ "$out" != *"updated $d/workshop.env"* && "$out" == *"PROJECT_ID is empty"* ]]
}

t_install_substrate_and_catchup_preserve_cache_with_prod_sim_ready() {
  local d; d="$(mktemp -d "$TMP/inst-cache.XXXXXX")"
  mkdir -p "$d/scripts" "$d/state/substrate/.git" "$d/bin" "$d/mockbin"
  cp "$ROOT/scripts/lib.sh" "$ROOT/scripts/install-substrate.sh" "$ROOT/scripts/catch-up.sh" "$d/scripts/"
  chmod +x "$d/scripts/install-substrate.sh" "$d/scripts/catch-up.sh"

  cat >"$d/env" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_NAME=ate-cluster
CLUSTER_LOCATION=us-central1-a
SUBSTRATE_REF=v1.0.0
SUBSTRATE_IMAGE_REPO=ghcr.io/agent-substrate/substrate
SUBSTRATE_IMAGE_TAG=v1.0.0
EOF

  printf '#!/usr/bin/env bash\nexit 0\n' >"$d/scripts/patch-router.sh"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$d/scripts/preflight.sh"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$d/scripts/gcp-resources.sh"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$d/scripts/prod-sim.sh"
  chmod +x "$d/scripts/"*.sh

  cat >"$d/mockbin/git" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  *"describe "*) echo "v1.0.0" ;;
  *) exit 0 ;;
esac
STUB
  cat >"$d/mockbin/go" <<'STUB'
#!/usr/bin/env bash
out=""
while (( $# > 0 )); do
  if [[ "$1" == "-o" ]]; then out="$2"; shift 2; continue; fi
  shift
done
if [[ -n "$out" ]]; then
  mkdir -p "$(dirname "$out")"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$out"
  chmod +x "$out"
fi
STUB
  cat >"$d/mockbin/kubectl" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  *"get deploy ate-api-server -o json"*|*"get deploy atenet-router -o json"*)
    echo '{"spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/agent-substrate/substrate/ate-api-server:v1.0.0@sha256:aaa","args":[]}]}}},"status":{"conditions":[{"type":"Available","status":"True"}]}}'
    ;;
  *"get ds atelet -o json"*)
    echo '{"spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/agent-substrate/substrate/atelet:v1.0.0@sha256:bbb"}]}}},"status":{"desiredNumberScheduled":2,"numberReady":2}}'
    ;;
  *"get workerpools.ate.dev -A -o json"*)
    echo '{"items":[{"spec":{"workerImage":"ghcr.io/agent-substrate/substrate/ateworker-gvisor:v1.0.0@sha256:ccc"}}]}'
    ;;
  *) exit 0 ;;
esac
STUB
  printf '#!/usr/bin/env bash\nexit 0\n' >"$d/mockbin/make"
  chmod +x "$d/mockbin/"*

  # Matching SUBSTRATE_REF followed by PROD_SIM=ready must preserve gateway-image & agent-image.
  printf '2026-01-01T00:00:00Z SUBSTRATE_REF=v1.0.0 PROD_SIM=ready\n' >"$d/state/install-complete"
  printf 'r/gateway@sha256:111\n' >"$d/state/gateway-image"
  printf 'r/agent@sha256:222\n' >"$d/state/agent-image"
  PATH="$d/mockbin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$d/scripts/install-substrate.sh" >/dev/null 2>&1 || return 1
  [[ -s "$d/state/gateway-image" && -s "$d/state/agent-image" ]] || return 1

  # Prefix-colliding SUBSTRATE_REF (v1.0.0-rc.1 vs v1.0.0) must clear cached image digests.
  printf '2026-01-01T00:00:00Z SUBSTRATE_REF=v1.0.0-rc.1 PROD_SIM=ready\n' >"$d/state/install-complete"
  PATH="$d/mockbin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$d/scripts/install-substrate.sh" >/dev/null 2>&1 || return 1
  [[ ! -f "$d/state/gateway-image" && ! -f "$d/state/agent-image" ]] || return 1

  # catch-up.sh skips install-substrate.sh when install-complete has SUBSTRATE_REF=v1.0.0 PROD_SIM=ready.
  printf '#!/usr/bin/env bash\necho "ran-install" >>"$WORKSHOP_STATE_DIR/cu.log"\n' >"$d/scripts/install-substrate.sh"
  chmod +x "$d/scripts/install-substrate.sh"
  printf '2026-01-01T00:00:00Z SUBSTRATE_REF=v1.0.0 PROD_SIM=ready\n' >"$d/state/install-complete"
  printf '2026-01-01T00:00:00Z PROJECT=demo-proj NAMESPACE=workshop-301 SPIFFE=principal://iam.googleapis.com/projects/0/locations/global/workloadIdentityPools/demo-proj.svc.id.goog/subject\n' >"$d/state/gcp-complete"
  printf 'r/ledger@sha256:111\n' >"$d/state/ledger-api-image"
  printf 'r/opshub@sha256:222\n' >"$d/state/ops-hub-image"
  : >"$d/state/cu.log"
  PATH="$d/mockbin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$d/scripts/catch-up.sh" 0 >/dev/null 2>&1 || return 1
  ! grep -Fq "ran-install" "$d/state/cu.log"
}

check "create-cluster.sh creates uniform-CPU c3-standard-4 GKE 1.37+ cluster, enables observability APIs, freezes upgrades, and fetches credentials" t_create_cluster_fresh_default_c3
check "create-cluster.sh defaults hyperdisk-balanced for n4/c4 and auto-pins Intel Ice Lake for n2" t_create_cluster_hyperdisk_for_n4_c4_and_autopin_n2
check "create-cluster.sh auto-pins Intel Skylake for n1-standard-4 pools and skips update when autoUpgrade is false" t_create_cluster_auto_pins_skylake_for_n1
check "create-cluster.sh freezes empty nodePools and removes expired workshop-freeze before re-adding" t_create_cluster_empty_nodepools_and_expired_exclusion
check "create-cluster.sh is idempotent when cluster exists with an active maintenance exclusion" t_create_cluster_idempotent_when_exists_with_active_exclusion
check "gcp-resources.sh enables core compute APIs, binds ate-snapshot-controller WI, and ensures REGIONAL_MANAGED_PROXY subnet" t_gcp_resources_core_apis_and_controller_wi
check "setup.sh auto-generates workshop.env, installs ko, pre-pulls Ready nodes only with Failed short-circuit, and skips cached work on rerun" t_setup_sh_orchestrates_full_bootstrap_and_idempotent_rerun
check "setup.sh derives REGION from zonal/regional CLUSTER_LOCATION and appends missing keys in existing workshop.env" t_setup_sh_derives_region_from_cluster_location
check "install-substrate.sh and catch-up.sh preserve cached images when install-complete includes PROD_SIM=ready" t_install_substrate_and_catchup_preserve_cache_with_prod_sim_ready
exit "$fails"
