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

# delete-cluster.sh against stub gcloud/kubectl: a wrong cluster name aborts before anything is deleted, the
# claims go before the cluster, and YES=1 skips the prompt. No cloud access needed.
# shellcheck disable=SC2329 # t_* run via check()
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
fails=0
check() { local name="$1"; shift; if "$@"; then echo "ok   $name"; else echo "FAIL $name"; fails=$((fails+1)); fi; }

mkdir -p "$TMP/bin"
cat >"$TMP/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "gcloud $*" >>"$STUB_LOG"
case "$*" in
  *"disks list"*) printf 'NAME     SIZE_GB  ZONE\npvc-123  10       us-central1-a\n' ;;
esac
exit 0
STUB
cat >"$TMP/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
echo "kubectl $*" >>"$STUB_LOG"
exit 0
STUB
chmod +x "$TMP/bin/"*
printf 'PROJECT_ID=p\nCLUSTER_NAME=c1\nCLUSTER_LOCATION=us-central1-a\n' >"$TMP/env"
# run [VAR=value...]: delete-cluster.sh with stdin from the caller; prints its output and leaves the stub log.
run() {
  : >"$TMP/log"
  env PATH="$TMP/bin:$PATH" STUB_LOG="$TMP/log" WORKSHOP_ENV_FILE="$TMP/env" WORKSHOP_STATE_DIR="$TMP/ws" "$@" \
    bash "$ROOT/scripts/delete-cluster.sh" 2>&1
}
# steps: the order of the calls that matter, from the stub log.
steps() {
  awk '/get-credentials/ {s = s " creds"} /delete statefulset postgres/ {s = s " sts"} /delete pvc --all/ {s = s " pvc"}
       /get pv / {s = s " pv"} /clusters delete/ {s = s " cluster"} /disks list/ {s = s " disks"}
       END {print substr(s, 2)}' "$TMP/log"
}

t_wrong_name_aborts() {
  local out; out="$(echo wrong | run)" && return 1
  [[ "$out" == *aborted* ]] && ! grep -q delete "$TMP/log"
}
t_typed_name_deletes_in_order() {
  local out; out="$(echo c1 | run)" || return 1
  [[ "$(steps)" == "creds sts pvc pv cluster disks" ]] &&
    grep -q 'clusters delete c1 --location us-central1-a --project p --quiet' "$TMP/log" &&
    [[ "$out" == *pvc-123* ]]
}
t_yes_skips_prompt() {
  run YES=1 </dev/null >/dev/null || return 1
  [[ "$(steps)" == "creds sts pvc pv cluster disks" ]]
}

check "a wrong cluster name aborts before anything is deleted" t_wrong_name_aborts
check "the typed cluster name deletes the claims, then the cluster, then lists leftover disks" t_typed_name_deletes_in_order
check "YES=1 skips the prompt" t_yes_skips_prompt
exit "$fails"
