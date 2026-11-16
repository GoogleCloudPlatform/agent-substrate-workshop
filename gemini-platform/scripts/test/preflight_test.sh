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

# Offline behavioral tests for scripts/preflight.sh, including uniform-CPU node-pool validation.
# shellcheck disable=SC2034 # $out is read inside check() eval strings
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
fails=0
check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; fails=$((fails+1)); fi; }

mkdir -p "$TMP/bin"
cat >"$TMP/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  "container clusters describe"*) cat "$CLUSTER_JSON" ;;
  "projects describe"*) echo 123456 ;;
esac
exit 0
STUB
cat >"$TMP/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  *certificates.k8s.io/v1beta1*) [[ -n "${V1_ONLY:-}" || -n "${NO_CERTS:-}" ]] && exit 1; echo '{"resources":[{"name":"clustertrustbundles"},{"name":"podcertificaterequests"}]}' ;;
  *certificates.k8s.io/v1*)      [[ -n "${NO_CERTS:-}" ]] && exit 1; echo '{"resources":[{"name":"clustertrustbundles"},{"name":"podcertificaterequests"}]}' ;;
esac
exit 0
STUB
printf '#!/usr/bin/env bash\n[[ -f go.mod ]] && echo go1.27.1 || echo go1.26.3\n' >"$TMP/bin/go"
printf '#!/usr/bin/env bash\nexit 0\n' >"$TMP/bin/ko"
printf '#!/usr/bin/env bash\n[[ "$1" == info && -n "${DOCKER_DOWN:-}" ]] && exit 1\nexit 0\n' >"$TMP/bin/docker"
chmod +x "$TMP/bin/"*
printf 'PROJECT_ID=demo-proj\nCLUSTER_NAME=c\nCLUSTER_LOCATION=us-central1-a\nSUBSTRATE_IMAGE_REPO=\nSUBSTRATE_IMAGE_TAG=\n' >"$TMP/env"
printf 'PROJECT_ID=demo-proj\nCLUSTER_NAME=c\nREGION=us-central1\nCLUSTER_LOCATION=\n' >"$TMP/noloc.env"

cat >"$TMP/good.json" <<'JSON'
{"currentMasterVersion": "1.37.2-gke.100", "description": "Sam's cluster", "resourceLabels": {"owner": "o'brien"},
 "workloadIdentityConfig": {"workloadPool": "demo-proj.svc.id.goog"},
 "managedOpentelemetryConfig": {"scope": "COLLECTION_AND_INSTRUMENTATION_COMPONENTS"},
 "monitoringConfig": {"managedPrometheusConfig": {"enabled": true}},
 "nodePools": [{"name": "default-pool", "management": {"autoUpgrade": false}, "config": {"machineType": "c3-standard-4", "spot": false}}]}
JSON
printf 'not json\n' >"$TMP/bad.json"

with_machine() {
  jq --arg mt "$1" --arg cpu "$2" '
    .nodePools[0].config.machineType = $mt
    | if $cpu != "" then .nodePools[0].config.minCpuPlatform = $cpu else del(.nodePools[0].config.minCpuPlatform) end
  ' "$TMP/good.json"
}

with_machine "n4-standard-4" "" >"$TMP/n4.json"
with_machine "n2-standard-4" "Intel Ice Lake" >"$TMP/n2-pinned.json"
with_machine "n2-standard-4" "" >"$TMP/n2-unpinned.json"
with_machine "n1-standard-4" "Intel Skylake" >"$TMP/n1-skylake.json"
with_machine "n1-standard-4" "" >"$TMP/n1-unpinned.json"
with_machine "e2-standard-4" "" >"$TMP/e2.json"

channel() { jq --arg s "$1" --arg e "$2" --arg scope "$3" '.nodePools[0].management.autoUpgrade = true
  | .maintenancePolicy.window.maintenanceExclusions.freeze = {startTime: $s, endTime: $e, maintenanceExclusionOptions: {scope: $scope}}' \
  "$TMP/good.json"; }
channel 2000-01-01T00:00:00Z 2999-01-01T00:00:00Z NO_MINOR_OR_NODE_UPGRADES >"$TMP/excluded.json"
channel 2000-01-01T00:00:00Z 2000-02-01T00:00:00Z NO_MINOR_OR_NODE_UPGRADES >"$TMP/expired.json"

run() { PATH="$TMP/bin:$PATH" WORKSHOP_ENV_FILE="$TMP/env" WORKSHOP_STATE_DIR="$TMP/ws" CLUSTER_JSON="$1" \
  bash "$ROOT/scripts/preflight.sh" 2>&1; }

out="$(run "$TMP/good.json")"; rc=$?
check "c3-standard-4 cluster with quotes in JSON passes" "[[ $rc == 0 ]]"
[[ $rc == 0 ]] || echo "$out"

out="$(run "$TMP/n4.json")"; rc=$?
check "n4-standard-4 uniform-CPU pool passes" "[[ $rc == 0 ]]"

out="$(run "$TMP/n2-pinned.json")"; rc=$?
check "CPU-pinned n2-standard-4 pool passes" "[[ $rc == 0 ]]"

out="$(run "$TMP/n2-unpinned.json")"; rc=$?
check "unpinned n2-standard-4 pool fails uniform-CPU check" "[[ $rc != 0 && \"\$out\" == *'uniform-CPU node pools'* ]]"

out="$(run "$TMP/n1-skylake.json")"; rc=$?
check "Google Skills Skylake-pinned n1-standard-4 pool passes" "[[ $rc == 0 ]]"

out="$(run "$TMP/n1-unpinned.json")"; rc=$?
check "unpinned n1-standard-4 pool fails uniform-CPU check" "[[ $rc != 0 && \"\$out\" == *'uniform-CPU node pools'* ]]"

out="$(run "$TMP/e2.json")"; rc=$?
check "mixed-CPU e2-standard-4 pool fails uniform-CPU check" "[[ $rc != 0 && \"\$out\" == *'uniform-CPU node pools'* ]]"

out="$(ALLOW_MIXED_CPU_FOR_TEST=true run "$TMP/e2.json")"; rc=$?
check "ALLOW_MIXED_CPU_FOR_TEST=true overrides mixed-CPU check with warning" "[[ $rc == 0 && \"\$out\" == *'ALLOW_MIXED_CPU_FOR_TEST'* ]]"

out="$(run "$TMP/bad.json")"; rc=$?
check "unreadable describe output fails cleanly" "[[ $rc != 0 && \"\$out\" == *'could not read the cluster version'* ]]"

out="$(run "$TMP/excluded.json")"; rc=$?
check "auto-upgrade under active node-upgrade exclusion passes" "[[ $rc == 0 ]]"

out="$(run "$TMP/expired.json")"; rc=$?
check "expired maintenance exclusion fails" "[[ $rc != 0 && \"\$out\" == *'no node auto-upgrade'* ]]"

out="$(V1_ONLY=1 run "$TMP/good.json")"; rc=$?
check "1.37 serving certificates.k8s.io/v1 passes" "[[ $rc == 0 ]]"

out="$(NO_CERTS=1 run "$TMP/good.json")"; rc=$?
check "missing certificates API fails" "[[ $rc != 0 && \"\$out\" == *'PodCertificateRequest served'* ]]"

jq '.currentMasterVersion = "1.36.4-gke.200"' "$TMP/good.json" >"$TMP/old.json"
out="$(run "$TMP/old.json")"; rc=$?
check "GKE 1.36 fails version check" "[[ $rc != 0 && \"\$out\" == *'>= 1.37'* ]]"

out="$(DOCKER_DOWN=1 run "$TMP/good.json")"; rc=$?
check "source install with stopped Docker fails" "[[ $rc != 0 && \"\$out\" == *'docker daemon reachable'* ]]"

out="$(DOCKER_DOWN=1 SUBSTRATE_IMAGE_REPO=r SUBSTRATE_IMAGE_TAG=t run "$TMP/good.json")"; rc=$?
check "published images skip Docker checks" "[[ $rc == 0 && \"\$out\" != *docker* ]]"

out="$(PATH="$TMP/bin:$PATH" WORKSHOP_ENV_FILE="$TMP/noloc.env" WORKSHOP_STATE_DIR="$TMP/ws" CLUSTER_JSON="$TMP/good.json" \
  bash "$ROOT/scripts/preflight.sh" 2>&1)"; rc=$?
check "empty CLUSTER_LOCATION fails with gcloud command hint" "[[ $rc != 0 && \"\$out\" == *'CLUSTER_LOCATION is empty'*'gcloud container clusters list'* ]]"

exit "$fails"
