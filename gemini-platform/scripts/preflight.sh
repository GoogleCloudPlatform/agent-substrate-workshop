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

# Checks local CLI tools, gcloud auth, uniform-CPU node pools, and GKE 1.37+ cluster prerequisites.
source "$(dirname "$0")/lib.sh"
load_env
require_cluster_location
failures=0

need() { if "$@"; then ok "$CHECK"; else warn "$CHECK — $HINT"; failures=$((failures+1)); fi; }

step "Tools"
for t in gcloud kubectl go ko jq envsubst curl git python3; do
  case "$t" in
    ko)       hint="install ko: go install github.com/google/ko@v0.19.1 (and add \$(go env GOPATH)/bin to PATH)" ;;
    envsubst) hint="install envsubst (gettext: brew install gettext / apt install gettext-base)" ;;
    *)        hint="install $t" ;;
  esac
  CHECK="$t on PATH" HINT="$hint"; need bash -c "command -v $t >/dev/null"
done

CHECK="go >= 1.27" HINT="install Go 1.27+ (or set GOTOOLCHAIN=auto so Go uses app/go.mod)"
need bash -c '[[ "$(printf "%s\n" "go1.27" "$(cd "$1/app" && go env GOVERSION 2>/dev/null)" | sort -V | head -1)" == "go1.27" ]]' _ "$REPO_ROOT"

if [[ -z "${SUBSTRATE_IMAGE_REPO:-}" ]]; then
  CHECK="docker on PATH" HINT="install Docker or keep SUBSTRATE_IMAGE_REPO set in workshop.env"
  need bash -c 'command -v docker >/dev/null'
  CHECK="docker daemon reachable" HINT="start the Docker daemon: docker info"
  need bash -c 'docker info >/dev/null 2>&1'
  CHECK="docker buildx available" HINT="install the docker buildx plugin: docker buildx version"
  need bash -c 'docker buildx version >/dev/null 2>&1'
fi

step "gcloud"
CHECK="gcloud authenticated" HINT="run: gcloud auth login && gcloud auth application-default login"
need bash -c 'gcloud auth print-access-token >/dev/null 2>&1'
CHECK="project ${PROJECT_ID} reachable" HINT="check PROJECT_ID and IAM roles"
need bash -c "gcloud projects describe '$PROJECT_ID' --format='value(projectNumber)' >/dev/null"

step "Cluster ${CLUSTER_NAME} (${CLUSTER_LOCATION})"
desc="$(gcloud container clusters describe "$CLUSTER_NAME" --location "$CLUSTER_LOCATION" --project "$PROJECT_ID" --format=json 2>/dev/null || true)"
[[ -n "$desc" ]] || die "cluster $CLUSTER_NAME not found in $CLUSTER_LOCATION; set CLUSTER_NAME / CLUSTER_LOCATION in workshop.env"
export DESC="$desc"
ver="$(jq -r '.currentMasterVersion // empty' <<<"$desc" 2>/dev/null)" || true
[[ -n "$ver" ]] || die "could not read the cluster version from 'gcloud container clusters describe' (is jq installed?)"
export VER="${ver%%-*}"

CHECK="GKE version $ver >= 1.37" HINT="recreate the cluster on GKE 1.37+"
need bash -c '[[ "$(printf "%s\n" 1.37 "$VER" | sort -V | head -1)" == 1.37 ]]'

CHECK="Workload Identity enabled" HINT="gcloud container clusters update $CLUSTER_NAME --location $CLUSTER_LOCATION --workload-pool=${PROJECT_ID}.svc.id.goog"
need bash -c 'jq -e --arg p "${PROJECT_ID}.svc.id.goog" ".workloadIdentityConfig.workloadPool == \$p" <<<"$DESC" >/dev/null 2>&1'

CHECK="managed OpenTelemetry collection enabled" HINT="gcloud beta container clusters update $CLUSTER_NAME --location $CLUSTER_LOCATION --managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS"
need bash -c 'jq -e ".managedOpentelemetryConfig.scope == \"COLLECTION_AND_INSTRUMENTATION_COMPONENTS\"" <<<"$DESC" >/dev/null 2>&1'

CHECK="Managed Prometheus enabled" HINT="gcloud container clusters update $CLUSTER_NAME --location $CLUSTER_LOCATION --enable-managed-prometheus"
need bash -c 'jq -e ".monitoringConfig.managedPrometheusConfig.enabled == true" <<<"$DESC" >/dev/null 2>&1'

CHECK="no node auto-upgrade" HINT="disable node auto-upgrade or add a maintenance exclusion with scope no_minor_or_node_upgrades"
need bash -c 'jq -e "([.nodePools[]? | (.management.autoUpgrade // false)] | (length > 0) and all(. == false))
  or ((now | todate) as \$t | [.maintenancePolicy.window.maintenanceExclusions // {} | .[]
      | select(.maintenanceExclusionOptions.scope == \"NO_MINOR_OR_NODE_UPGRADES\" or .maintenanceExclusionOptions.scope == \"NO_UPGRADES\")
      | select(.startTime <= \$t and \$t < .endTime)] | length > 0)" <<<"$DESC" >/dev/null 2>&1'

CHECK="no spot/preemptible nodes" HINT="use standard non-spot node pools"
need bash -c 'jq -e "[.nodePools[].config | (.spot // false) or (.preemptible // false)] | all(. == false)" <<<"$DESC" >/dev/null 2>&1'

# Cross-worker SNAPSHOT_FIDELITY_MEMORY restores require uniform CPU features across worker nodes.
# Accept c3-*, c3d-*, c4-*, n4-*, CPU-pinned n2-*/n2d-*, and Skylake+-pinned n1-* (Google Skills).
check_uniform_cpu() {
  jq -e '
    def skylake_plus($p):
      $p == "Intel Skylake" or $p == "Intel Cascade Lake" or $p == "Intel Ice Lake" or $p == "Intel Sapphire Rapids" or $p == "Intel Emerald Rapids";
    [ .nodePools[]
      | (.config.machineType // "") as $mt
      | (.config.minCpuPlatform // "") as $cpu
      | (
          ($mt | test("^(c3|c3d|c4|n4)-"))
          or (($mt | test("^(n2|n2d)-")) and ($cpu != "" and $cpu != "AUTOMATIC"))
          or (($mt | test("^n1-")) and skylake_plus($cpu))
        )
    ] | (length > 0) and all(. == true)
  ' <<<"$DESC" >/dev/null 2>&1
}

if [[ "${ALLOW_MIXED_CPU_FOR_TEST:-}" == "true" ]]; then
  if check_uniform_cpu; then
    ok "uniform-CPU node pools"
  else
    warn "ALLOW_MIXED_CPU_FOR_TEST=true: bypassing uniform-CPU node-pool check (cross-worker MEMORY restores may fall back on mixed e2 CPUs)"
  fi
else
  CHECK="uniform-CPU node pools" HINT="use c3-standard-4, n4-standard-4, CPU-pinned n2-standard-4 (--min-cpu-platform), or Skylake-pinned n1-standard-4; mixed-CPU e2 pools break cross-worker memory snapshot restores"
  need check_uniform_cpu
fi

step "kubectl context"
gcloud container clusters get-credentials "$CLUSTER_NAME" --location "$CLUSTER_LOCATION" --project "$PROJECT_ID" >/dev/null 2>&1 || true
CHECK="kubectl reaches the cluster" HINT="gcloud container clusters get-credentials $CLUSTER_NAME --location $CLUSTER_LOCATION --project $PROJECT_ID"
need bash -c "kubectl get nodes >/dev/null"

CHECK="certificates.k8s.io ClusterTrustBundle + PodCertificateRequest served" HINT="use GKE 1.37+"
need bash -c 'r="$(kubectl get --raw /apis/certificates.k8s.io/v1 2>/dev/null; kubectl get --raw /apis/certificates.k8s.io/v1beta1 2>/dev/null)"; [[ "$r" == *\"clustertrustbundles\"* && "$r" == *\"podcertificaterequests\"* ]]'

(( failures == 0 )) || die "$failures preflight check(s) failed"
ok "preflight passed"
