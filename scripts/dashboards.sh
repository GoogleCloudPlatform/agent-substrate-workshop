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

# Creates or updates the workshop dashboard and substrate's three dashboards in Cloud Monitoring.
# Dashboards are matched by displayName, so re-running updates in place instead of duplicating.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# upsert_dashboard <json-file>
upsert_dashboard() {
  local file="$1"
  local title existing name etag
  title="$(jq -r .displayName "$file")"
  existing="$(gcloud monitoring dashboards list --project "$PROJECT_ID" \
    --filter="displayName=\"$title\"" --format='value(name,etag)' | head -n1)"
  if [[ -z "$existing" ]]; then
    gcloud monitoring dashboards create --project "$PROJECT_ID" --config-from-file="$file" --quiet >/dev/null
    ok "created dashboard: $title"
  else
    read -r name etag <<<"$existing"
    jq --arg etag "$etag" '. + {etag: $etag}' "$file" \
      | gcloud monitoring dashboards update "$name" --project "$PROJECT_ID" --config-from-file=- --quiet >/dev/null
    ok "updated dashboard: $title"
  fi
}

[[ "${1:-}" == "--source-only" ]] && return 0

load_env
step "Cloud Monitoring dashboards"
upsert_dashboard "$REPO_ROOT/observability/dashboard.json"
# The substrate checkout is created early by install-substrate.sh; wait briefly if the install just started.
wait_for 180 "substrate checkout (make install-bg)" test -d "$STATE_DIR/substrate/tools/setup-gcp/dashboards"
for f in "$STATE_DIR"/substrate/tools/setup-gcp/dashboards/*.json; do
  upsert_dashboard "$f"
done
ok "open https://console.cloud.google.com/monitoring/dashboards?project=$PROJECT_ID"
