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

# preflight.sh against stub gcloud/kubectl/go/ko: the cluster JSON may contain quotes, and a broken
# describe output fails with one clear line instead of a jq error. No cloud access needed.
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
  "projects describe"*) echo 123 ;;
esac
exit 0
STUB
cat >"$TMP/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
case "$*" in *certificates.k8s.io/v1beta1*) [[ -n "${V1_ONLY:-}" ]] && exit 1; echo '{"resources":[{"name":"clustertrustbundles"},{"name":"podcertificaterequests"}]}' ;; esac
exit 0
STUB
printf '#!/usr/bin/env bash\necho go1.27.1\n' >"$TMP/bin/go"
printf '#!/usr/bin/env bash\nexit 0\n' >"$TMP/bin/ko"
chmod +x "$TMP/bin/"*
printf 'PROJECT_ID=demo-proj\nCLUSTER_NAME=c\nCLUSTER_LOCATION=r\n' >"$TMP/env"

# A cluster that passes every check, with a quote in a label and in the description.
cat >"$TMP/good.json" <<'JSON'
{"currentMasterVersion": "1.37.2-gke.100", "description": "Sam's cluster", "resourceLabels": {"owner": "o'brien"},
 "workloadIdentityConfig": {"workloadPool": "demo-proj.svc.id.goog"},
 "managedOpentelemetryConfig": {"scope": "COLLECTION_AND_INSTRUMENTATION_COMPONENTS"},
 "monitoringConfig": {"managedPrometheusConfig": {"enabled": true}},
 "nodePools": [{"management": {"autoUpgrade": false}, "config": {"spot": false}}]}
JSON
printf 'not json\n' >"$TMP/bad.json"
# Release-channel clusters: auto-upgrade is forced on, so an active node-upgrade exclusion must stand in for it.
channel() { jq --arg s "$1" --arg e "$2" --arg scope "$3" '.nodePools[0].management.autoUpgrade = true
  | .maintenancePolicy.window.maintenanceExclusions.freeze = {startTime: $s, endTime: $e, maintenanceExclusionOptions: {scope: $scope}}' \
  "$TMP/good.json"; }
channel 2000-01-01T00:00:00Z 2999-01-01T00:00:00Z NO_MINOR_OR_NODE_UPGRADES >"$TMP/excluded.json"
channel 2000-01-01T00:00:00Z 2000-02-01T00:00:00Z NO_MINOR_OR_NODE_UPGRADES >"$TMP/expired.json"
channel 2000-01-01T00:00:00Z 2999-01-01T00:00:00Z NO_MINOR_UPGRADES >"$TMP/minor-only.json"

run() { PATH="$TMP/bin:$PATH" WORKSHOP_ENV_FILE="$TMP/env" WORKSHOP_STATE_DIR="$TMP/ws" CLUSTER_JSON="$1" \
  bash "$ROOT/scripts/preflight.sh" 2>&1; }
out="$(run "$TMP/good.json")"; rc=$?
check "quotes in the cluster JSON don't break the checks" "[[ $rc == 0 ]]"
[[ $rc == 0 ]] || echo "$out"
out="$(run "$TMP/bad.json")"; rc=$?
check "unreadable describe output fails with one clear line" "[[ $rc != 0 && \"\$out\" == *'could not read the cluster version'* ]]"
out="$(run "$TMP/excluded.json")"; rc=$?
check "auto-upgrade under an active node-upgrade exclusion passes" "[[ $rc == 0 ]]"
[[ $rc == 0 ]] || echo "$out"
out="$(run "$TMP/expired.json")"; rc=$?
check "an expired exclusion doesn't count" "[[ $rc != 0 && \"\$out\" == *'no node auto-upgrade —'* ]]"
out="$(run "$TMP/minor-only.json")"; rc=$?
check "a minor-only exclusion doesn't count (nodes still upgrade)" "[[ $rc != 0 && \"\$out\" == *'no node auto-upgrade —'* ]]"
out="$(V1_ONLY=1 run "$TMP/good.json")"; rc=$?
check "1.37 serving only certificates.k8s.io/v1 fails the v1beta1 check" "[[ $rc != 0 && \"\$out\" == *'PodCertificateRequest served —'* ]]"
jq '.currentMasterVersion = "1.36.4-gke.200" | .enableK8sBetaApis.enabledApis = ["certificates.k8s.io/v1beta1/clustertrustbundles", "certificates.k8s.io/v1beta1/podcertificaterequests"]' \
  "$TMP/good.json" >"$TMP/old.json"
out="$(run "$TMP/old.json")"; rc=$?
check "1.36 fails the version check, even with the beta APIs" "[[ $rc != 0 && \"\$out\" == *'>= 1.37 —'* ]]"
exit "$fails"
