#!/usr/bin/env bash
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
case "$*" in *api-resources*) echo clustertrustbundles.certificates.k8s.io ;; esac
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

run() { PATH="$TMP/bin:$PATH" WORKSHOP_ENV_FILE="$TMP/env" WORKSHOP_STATE_DIR="$TMP/ws" CLUSTER_JSON="$1" \
  bash "$ROOT/scripts/preflight.sh" 2>&1; }
out="$(run "$TMP/good.json")"; rc=$?
check "quotes in the cluster JSON don't break the checks" "[[ $rc == 0 ]]"
[[ $rc == 0 ]] || echo "$out"
out="$(run "$TMP/bad.json")"; rc=$?
check "unreadable describe output fails with one clear line" "[[ $rc != 0 && \"\$out\" == *'could not read the cluster version'* ]]"
exit "$fails"
