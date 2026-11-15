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

# Baseline ledger-api connection pool diagnostic (SKILL_REVISION=v1).
# Inspects config/pool.yaml inside the working directory (or $1).
set -eu

TARGET_DIR="${1:-.}"
POOL_YAML="${TARGET_DIR}/config/pool.yaml"

if [ ! -f "${POOL_YAML}" ]; then
  echo "SKILL_NAME=ledger-pool-triage"
  echo "SKILL_REVISION=v1"
  echo "POOL_CONFIG_STATUS=MISSING_POOL_YAML"
  printf '{"skill":"ledger-pool-triage","skill_revision":"v1","error":"missing %s"}\n' "${POOL_YAML}"
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

echo "SKILL_NAME=ledger-pool-triage"
echo "SKILL_REVISION=v1"
echo "CHECK_SCOPE=pool_config_baseline"
echo "MAX_OPEN_CONNS=${MAX_OPEN}"
echo "IDLE_TIMEOUT_SECONDS=${IDLE_TIMEOUT}"
echo "ACQUIRE_TIMEOUT_MS=${ACQUIRE_MS}"
echo "POOL_CONFIG_STATUS=${POOL_STATUS}"
printf '{"skill":"ledger-pool-triage","skill_revision":"v1","scope":"pool_config_baseline","max_open_conns":%s,"idle_timeout_seconds":%s,"acquire_timeout_ms":%s,"pool_config_ok":%s}\n' \
  "${MAX_OPEN}" "${IDLE_TIMEOUT}" "${ACQUIRE_MS}" "${POOL_OK}"
