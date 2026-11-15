#!/bin/sh
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

# Enhanced ledger-api connection pool + Go cursor-leak diagnostic (SKILL_REVISION=v2).
# Inspects both config/pool.yaml and Go source files (pool/store.go) for unclosed cursors.
set -eu

TARGET_DIR="${1:-.}"
POOL_YAML="${TARGET_DIR}/config/pool.yaml"
STORE_GO="${TARGET_DIR}/pool/store.go"

if [ ! -f "${POOL_YAML}" ]; then
  echo "SKILL_NAME=ledger-pool-triage"
  echo "SKILL_REVISION=v2"
  echo "POOL_CONFIG_STATUS=MISSING_POOL_YAML"
  printf '{"skill":"ledger-pool-triage","skill_revision":"v2","error":"missing %s"}\n' "${POOL_YAML}"
  exit 1
fi

read_yaml_int() {
  key="$1"
  awk -F: -v k="$key" '
    {
      sub(/#.*/, "", $0)
      gsub(/^[ \t]+|[ \t]+$/, "", $1)
      if ($1 == k) {
        gsub(/^[ \t]+|[ \t]+$/, "", $2)
        print $2
        exit
      }
    }
  ' "${POOL_YAML}"
}

MAX_OPEN="$(read_yaml_int max_open_conns)"
IDLE_TIMEOUT="$(read_yaml_int idle_timeout_seconds)"
ACQUIRE_MS="$(read_yaml_int acquire_timeout_ms)"

: "${MAX_OPEN:=0}"
: "${IDLE_TIMEOUT:=0}"
: "${ACQUIRE_MS:=0}"

POOL_STATUS="OK"
POOL_OK="true"
if [ "${MAX_OPEN}" -lt 16 ] || [ "${IDLE_TIMEOUT}" -lt 5 ]; then
  POOL_STATUS="SATURATION_RISK"
  POOL_OK="false"
fi

LEAK_DETECTED="false"
LEAK_LOCATION="none"
LEAK_DETAIL="defer rows.Close() precedes ValidateChecksum() in ReconcileTransfer"

if [ -f "${STORE_GO}" ]; then
  leak_state="$(awk '
    /^func .*ReconcileTransfer\(/ { in_fn = 1; acquired = 0; deferred = 0; leak = 0; next }
    in_fn && /^}/ { in_fn = 0 }
    in_fn {
      if ($0 ~ /AcquireRows\(/) { acquired = 1 }
      if (acquired && $0 ~ /defer[ \t]+[A-Za-z0-9_]+\.Close\(\)/) { deferred = 1 }
      if (acquired && $0 ~ /ValidateChecksum\(\)/ && !deferred) { leak = 1 }
    }
    END {
      if (leak) { print "LEAK" } else { print "CLEAN" }
    }
  ' "${STORE_GO}")"
  if [ "${leak_state}" = "LEAK" ]; then
    LEAK_DETECTED="true"
    LEAK_LOCATION="pool/store.go:ReconcileTransfer"
    LEAK_DETAIL="rows.ValidateChecksum() returns on checksum error before rows.Close() releases the pool slot"
  fi
fi

echo "SKILL_NAME=ledger-pool-triage"
echo "SKILL_REVISION=v2"
echo "CHECK_SCOPE=pool_config_and_cursor_leak"
echo "MAX_OPEN_CONNS=${MAX_OPEN}"
echo "IDLE_TIMEOUT_SECONDS=${IDLE_TIMEOUT}"
echo "ACQUIRE_TIMEOUT_MS=${ACQUIRE_MS}"
echo "POOL_CONFIG_STATUS=${POOL_STATUS}"
echo "CURSOR_LEAK_DETECTED=${LEAK_DETECTED}"
echo "CURSOR_LEAK_LOCATION=${LEAK_LOCATION}"
echo "CURSOR_LEAK_DETAIL=${LEAK_DETAIL}"
printf '{"skill":"ledger-pool-triage","skill_revision":"v2","scope":"pool_config_and_cursor_leak","max_open_conns":%s,"idle_timeout_seconds":%s,"acquire_timeout_ms":%s,"pool_config_ok":%s,"cursor_leak_detected":%s,"cursor_leak_location":"%s"}\n' \
  "${MAX_OPEN}" "${IDLE_TIMEOUT}" "${ACQUIRE_MS}" "${POOL_OK}" "${LEAK_DETECTED}" "${LEAK_LOCATION}"
