#!/usr/bin/env bash
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
  CHECK="$t on PATH" HINT="install $t"; need bash -c "command -v $t >/dev/null"
done
CHECK="go >= 1.27" HINT="install Go 1.27+"
need bash -c '[[ "$(printf "%s\n" "go1.27" "$(go env GOVERSION)" | sort -V | head -1)" == "go1.27" ]]'

step "gcloud"
CHECK="gcloud authenticated" HINT="run: gcloud auth login && gcloud auth application-default login"
need bash -c 'gcloud auth print-access-token >/dev/null 2>&1'
CHECK="project ${PROJECT_ID} reachable" HINT="check PROJECT_ID and your Owner role"
need bash -c "gcloud projects describe '$PROJECT_ID' --format='value(projectNumber)' >/dev/null"

step "Cluster ${CLUSTER} (${ZONE_OR_REGION})"
desc="$(gcloud container clusters describe "$CLUSTER" --location "$ZONE_OR_REGION" --project "$PROJECT_ID" --format=json 2>/dev/null || true)"
[[ -n "$desc" ]] || die "cluster $CLUSTER not found in $ZONE_OR_REGION — set CLUSTER / ZONE_OR_REGION in workshop.env"
# The checks below read the JSON from the environment, never spliced into a quoted string: labels or a
# description containing a quote would otherwise break them.
export DESC="$desc"
ver="$(jq -r '.currentMasterVersion // empty' <<<"$desc" 2>/dev/null)" || true
[[ -n "$ver" ]] || die "could not read the cluster version from 'gcloud container clusters describe' (is jq installed?)"
export VER="${ver%%-*}"
CHECK="GKE version $ver >= 1.37 (or 1.36 with beta APIs)" HINT="recreate the cluster on 1.37+"
need bash -c '[[ "$(printf "%s\n" 1.36 "$VER" | sort -V | head -1)" == 1.36 ]]'
if [[ "$(printf '%s\n' 1.37 "$VER" | sort -V | head -1)" != 1.37 ]]; then
  CHECK="1.36 beta APIs enabled" HINT="1.36 needs --enable-kubernetes-unstable-apis for podcertificaterequests and clustertrustbundles at creation"
  need bash -c 'jq -e ".enableK8sBetaApis.enabledApis // [] | map(select(test(\"clustertrustbundles|podcertificaterequests\"))) | length == 2" <<<"$DESC" >/dev/null 2>&1'
fi
CHECK="Workload Identity enabled" HINT="gcloud container clusters update $CLUSTER --workload-pool=${PROJECT_ID}.svc.id.goog"
need bash -c 'jq -e --arg p "${PROJECT_ID}.svc.id.goog" ".workloadIdentityConfig.workloadPool == \$p" <<<"$DESC" >/dev/null 2>&1'
CHECK="managed OpenTelemetry collection enabled" HINT="gcloud container clusters update $CLUSTER --managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS"
need bash -c 'jq -e ".managedOpentelemetryConfig.scope == \"COLLECTION_AND_INSTRUMENTATION_COMPONENTS\"" <<<"$DESC" >/dev/null 2>&1'
CHECK="Managed Prometheus enabled" HINT="gcloud container clusters update $CLUSTER --enable-managed-prometheus"
need bash -c 'jq -e ".monitoringConfig.managedPrometheusConfig.enabled == true" <<<"$DESC" >/dev/null 2>&1'
CHECK="no node auto-upgrade" HINT="gcloud container node-pools update <pool> --no-enable-autoupgrade"
need bash -c 'jq -e "[.nodePools[].management.autoUpgrade // false] | all(. == false)" <<<"$DESC" >/dev/null 2>&1'
CHECK="no spot/preemptible nodes" HINT="use a standard node pool"
need bash -c 'jq -e "[.nodePools[].config | (.spot // false) or (.preemptible // false)] | all(. == false)" <<<"$DESC" >/dev/null 2>&1'

step "kubectl context"
gcloud container clusters get-credentials "$CLUSTER" --location "$ZONE_OR_REGION" --project "$PROJECT_ID" >/dev/null 2>&1 || true
CHECK="kubectl reaches the cluster" HINT="gcloud container clusters get-credentials $CLUSTER --location $ZONE_OR_REGION"
need bash -c "kubectl get nodes >/dev/null"
CHECK="ClusterTrustBundle API served" HINT="needs GKE 1.37+ or the 1.36 beta API"
need bash -c 'kubectl api-resources --api-group=certificates.k8s.io -o name | grep -q clustertrustbundles'

(( failures == 0 )) || die "$failures preflight check(s) failed"
ok "preflight passed"
