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

# Idempotently enables core GKE APIs, creates a uniform-CPU GKE 1.37+ cluster
# (Workload Identity, Managed Prometheus, managed OpenTelemetry, Standard Gateway API,
# 100 GiB pd-balanced boot disks), freezes node auto-upgrades for 48 hours,
# and fetches kubectl credentials.
source "$(dirname "$0")/lib.sh"
load_env
require_cluster_location

CLUSTER_VERSION="${CLUSTER_VERSION:-1.37}"
CLUSTER_MACHINE_TYPE="${CLUSTER_MACHINE_TYPE:-c3-standard-4}"
CLUSTER_NUM_NODES="${CLUSTER_NUM_NODES:-2}"
CLUSTER_DISK_TYPE="${CLUSTER_DISK_TYPE:-pd-balanced}"
CLUSTER_DISK_SIZE="${CLUSTER_DISK_SIZE:-100}"
CLUSTER_MIN_CPU_PLATFORM="${CLUSTER_MIN_CPU_PLATFORM:-}"

# Pin n1-* node pools to Intel Skylake when caller did not specify CLUSTER_MIN_CPU_PLATFORM
# so SNAPSHOT_FIDELITY_MEMORY checkpoints restore across workers.
if [[ -z "$CLUSTER_MIN_CPU_PLATFORM" && "$CLUSTER_MACHINE_TYPE" == n1-* ]]; then
  CLUSTER_MIN_CPU_PLATFORM="Intel Skylake"
fi

step "Enable core compute and GKE APIs in ${PROJECT_ID}"
gcloud services enable --project "$PROJECT_ID" \
  serviceusage.googleapis.com \
  cloudresourcemanager.googleapis.com \
  compute.googleapis.com \
  container.googleapis.com
ok "core cluster APIs enabled"

desc="$(gcloud container clusters describe "$CLUSTER_NAME" \
  --location "$CLUSTER_LOCATION" \
  --project "$PROJECT_ID" \
  --format=json 2>/dev/null || true)"

if [[ -n "$desc" ]]; then
  ok "cluster ${CLUSTER_NAME} already exists in ${CLUSTER_LOCATION}; skipping creation"
else
  step "Create uniform-CPU GKE ${CLUSTER_VERSION} cluster ${CLUSTER_NAME} (${CLUSTER_LOCATION}, ${CLUSTER_NUM_NODES}x ${CLUSTER_MACHINE_TYPE})"
  create_args=(
    --project "$PROJECT_ID"
    --location "$CLUSTER_LOCATION"
    --release-channel rapid
    --cluster-version "$CLUSTER_VERSION"
    --machine-type "$CLUSTER_MACHINE_TYPE"
    --num-nodes "$CLUSTER_NUM_NODES"
    --disk-type "$CLUSTER_DISK_TYPE"
    --disk-size "$CLUSTER_DISK_SIZE"
    --scopes=https://www.googleapis.com/auth/cloud-platform
    --gateway-api=standard
    --workload-pool="${PROJECT_ID}.svc.id.goog"
    --enable-managed-prometheus
    --managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS
  )
  if [[ -n "$CLUSTER_MIN_CPU_PLATFORM" ]]; then
    create_args+=(--min-cpu-platform "$CLUSTER_MIN_CPU_PLATFORM")
  fi
  if ! gcloud beta container clusters create "$CLUSTER_NAME" "${create_args[@]}"; then
    warn "cluster creation failed for machine type ${CLUSTER_MACHINE_TYPE} in ${CLUSTER_LOCATION}."
    warn "If your zone lacks ${CLUSTER_MACHINE_TYPE} quota or stock, retry with:"
    warn "  CLUSTER_MACHINE_TYPE=n4-standard-4 make create-cluster"
    warn "  CLUSTER_MACHINE_TYPE=n1-standard-4 CLUSTER_MIN_CPU_PLATFORM='Intel Skylake' make create-cluster"
    exit 1
  fi
  desc="$(gcloud container clusters describe "$CLUSTER_NAME" \
    --location "$CLUSTER_LOCATION" \
    --project "$PROJECT_ID" \
    --format=json 2>/dev/null || true)"
  ok "cluster ${CLUSTER_NAME} created"
fi

has_upgrade_freeze() {
  [[ -n "${1:-}" ]] || return 1
  jq -e '
    ([.nodePools[]?.management.autoUpgrade // false] | all(. == false))
    or ((now | todate) as $t | [.maintenancePolicy.window.maintenanceExclusions // {} | .[]
        | select(.maintenanceExclusionOptions.scope == "NO_MINOR_OR_NODE_UPGRADES" or .maintenanceExclusionOptions.scope == "NO_UPGRADES")
        | select(.startTime <= $t and $t < .endTime)] | length > 0)
  ' <<<"$1" >/dev/null 2>&1
}

if has_upgrade_freeze "$desc"; then
  ok "node auto-upgrade disabled or active maintenance exclusion present"
else
  step "Add 48-hour maintenance exclusion (workshop-freeze) on ${CLUSTER_NAME}"
  freeze_start="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  freeze_end="$(date -u -d '+2 days' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+2d +%Y-%m-%dT%H:%M:%SZ)"
  gcloud container clusters update "$CLUSTER_NAME" \
    --project "$PROJECT_ID" \
    --location "$CLUSTER_LOCATION" \
    --add-maintenance-exclusion-name workshop-freeze \
    --add-maintenance-exclusion-start "$freeze_start" \
    --add-maintenance-exclusion-end "$freeze_end" \
    --add-maintenance-exclusion-scope no_minor_or_node_upgrades
  ok "maintenance exclusion active through ${freeze_end}"
fi

step "Fetch kubectl credentials for ${CLUSTER_NAME} (${CLUSTER_LOCATION})"
gcloud container clusters get-credentials "$CLUSTER_NAME" \
  --location "$CLUSTER_LOCATION" \
  --project "$PROJECT_ID"
ok "kubectl context configured for ${CLUSTER_NAME}"
