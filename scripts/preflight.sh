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

# Checks laptop tools and the GKE cluster before anything is installed.
source "$(dirname "$0")/lib.sh"
load_env
failures=0
# need <cmd...>: the checked command must silence its own stdout/stderr
# (e.g. via `bash -c '... >/dev/null'`) so only the ok/warn line prints —
# redirecting the outer `need` call instead would swallow the ok/warn output too.
need() { if "$@"; then ok "$CHECK"; else warn "$CHECK — $HINT"; failures=$((failures+1)); fi; }

step "Tools"
for t in gcloud kubectl go ko jq envsubst nc curl git python3; do
  case "$t" in
    ko)       hint="install ko: go install github.com/google/ko@latest (then make sure \$(go env GOPATH)/bin is on PATH)" ;;
    envsubst) hint="install envsubst (gettext: brew install gettext / apt install gettext-base)" ;;
    nc)       hint="install nc (netcat)" ;;
    *)        hint="install $t" ;;
  esac
  CHECK="$t on PATH" HINT="$hint"; need bash -c "command -v $t >/dev/null"
done
CHECK="go >= 1.27" HINT="install Go 1.27+"
need bash -c '[[ "$(printf "%s\n" "go1.27" "$(go env GOVERSION)" | sort -V | head -1)" == "go1.27" ]]'

step "gcloud"
CHECK="gcloud authenticated" HINT="run: gcloud auth login && gcloud auth application-default login"
need bash -c 'gcloud auth print-access-token >/dev/null 2>&1'
CHECK="project ${PROJECT_ID} reachable" HINT="check PROJECT_ID and your Owner role"
need bash -c "gcloud projects describe '$PROJECT_ID' --format='value(projectNumber)' >/dev/null"

step "Cluster ${CLUSTER_NAME} (${CLUSTER_LOCATION})"
desc="$(gcloud container clusters describe "$CLUSTER_NAME" --location "$CLUSTER_LOCATION" --project "$PROJECT_ID" --format=json 2>/dev/null || true)"
[[ -n "$desc" ]] || die "cluster $CLUSTER_NAME not found in $CLUSTER_LOCATION — set CLUSTER_NAME / CLUSTER_LOCATION in workshop.env"
# The checks below read the JSON from the environment, never spliced into a quoted string: labels or a
# description containing a quote would otherwise break them.
export DESC="$desc"
ver="$(jq -r '.currentMasterVersion // empty' <<<"$desc" 2>/dev/null)" || true
[[ -n "$ver" ]] || die "could not read the cluster version from 'gcloud container clusters describe' (is jq installed?)"
export VER="${ver%%-*}"
CHECK="GKE version $ver >= 1.37" HINT="recreate the cluster on 1.37+ (the workshop is validated on 1.37)"
need bash -c '[[ "$(printf "%s\n" 1.37 "$VER" | sort -V | head -1)" == 1.37 ]]'
CHECK="Workload Identity enabled" HINT="gcloud container clusters update $CLUSTER_NAME --workload-pool=${PROJECT_ID}.svc.id.goog"
need bash -c 'jq -e --arg p "${PROJECT_ID}.svc.id.goog" ".workloadIdentityConfig.workloadPool == \$p" <<<"$DESC" >/dev/null 2>&1'
CHECK="managed OpenTelemetry collection enabled" HINT="gcloud beta container clusters update $CLUSTER_NAME --managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS"
need bash -c 'jq -e ".managedOpentelemetryConfig.scope == \"COLLECTION_AND_INSTRUMENTATION_COMPONENTS\"" <<<"$DESC" >/dev/null 2>&1'
CHECK="Managed Prometheus enabled" HINT="gcloud container clusters update $CLUSTER_NAME --enable-managed-prometheus"
need bash -c 'jq -e ".monitoringConfig.managedPrometheusConfig.enabled == true" <<<"$DESC" >/dev/null 2>&1'
# New clusters must join a release channel, and a channel forces node auto-upgrade on, so an active
# maintenance exclusion that blocks node upgrades counts too.
CHECK="no node auto-upgrade" HINT="gcloud container node-pools update <pool> --no-enable-autoupgrade, or on a release channel: gcloud container clusters update $CLUSTER_NAME --add-maintenance-exclusion-scope no_minor_or_node_upgrades (plus -name/-start/-end)"
need bash -c 'jq -e "([.nodePools[].management.autoUpgrade // false] | all(. == false))
  or ((now | todate) as \$t | [.maintenancePolicy.window.maintenanceExclusions // {} | .[]
      | select(.maintenanceExclusionOptions.scope == \"NO_MINOR_OR_NODE_UPGRADES\" or .maintenanceExclusionOptions.scope == \"NO_UPGRADES\")
      | select(.startTime <= \$t and \$t < .endTime)] | length > 0)" <<<"$DESC" >/dev/null 2>&1'
CHECK="no spot/preemptible nodes" HINT="use a standard node pool"
need bash -c 'jq -e "[.nodePools[].config | (.spot // false) or (.preemptible // false)] | all(. == false)" <<<"$DESC" >/dev/null 2>&1'

step "kubectl context"
gcloud container clusters get-credentials "$CLUSTER_NAME" --location "$CLUSTER_LOCATION" --project "$PROJECT_ID" >/dev/null 2>&1 || true
CHECK="kubectl reaches the cluster" HINT="gcloud container clusters get-credentials $CLUSTER_NAME --location $CLUSTER_LOCATION"
need bash -c "kubectl get nodes >/dev/null"
# Substrate's ClusterTrustBundle code is v1beta1-only (PodCertificateRequest takes v1 or v1beta1, but its own
# GCP setup enables both, so we do too). GKE 1.37 serves both at v1 and drops v1beta1 unless it's enabled.
CHECK="certificates.k8s.io/v1beta1 ClusterTrustBundle + PodCertificateRequest served" HINT="gcloud container clusters update $CLUSTER_NAME --enable-kubernetes-unstable-apis=certificates.k8s.io/v1beta1/clustertrustbundles,certificates.k8s.io/v1beta1/podcertificaterequests"
need bash -c 'r="$(kubectl get --raw /apis/certificates.k8s.io/v1beta1 2>/dev/null)"; [[ "$r" == *\"clustertrustbundles\"* && "$r" == *\"podcertificaterequests\"* ]]'

(( failures == 0 )) || die "$failures preflight check(s) failed"
ok "preflight passed"
