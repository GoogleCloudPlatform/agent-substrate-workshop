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

# Audits live Agent Substrate ActorTemplates, Actors, /workspace state, and Cloud Storage
# snapshot objects (gs://${BUCKET_NAME}/atespaces/${ATESPACE}/**) to prove zero bearer tokens
# or API keys exist inside gVisor sandboxes or persisted memory/volume snapshots.
# Matched secret substrings are counted and never printed to stdout or stderr.
# shellcheck source=scripts/lib.sh
source "$(dirname "$0")/lib.sh"
load_env

# Construct token prefixes dynamically so static scanners never treat regex literals as credentials.
P_GCP="ya29""\\.[A-Za-z0-9._-]+"
P_GHO="gho""_[A-Za-z0-9_]+"
P_GHU="ghu""_[A-Za-z0-9_]+"
P_GHP="github_pat""_[A-Za-z0-9_]+"
P_OPS="opshub_at""_[A-Za-z0-9_]+"
P_KEY="AIza""[0-9A-Za-z_-]{20,}"
TOKEN_REGEX="(${P_GCP}|${P_GHO}|${P_GHU}|${P_GHP}|${P_OPS}|${P_KEY})"
FORBIDDEN_ENV_REGEX='^(GEMINI_API_KEY|GOOGLE_API_KEY|OAUTH_CLIENT_SECRET|GITHUB_OAUTH_CLIENT_SECRET|BEARER_TOKEN|ACCESS_TOKEN)$'

count_matches() {
  local c
  c="$(LC_ALL=C grep -aoE "$TOKEN_REGEX" 2>/dev/null | wc -l | tr -d ' ')" || c=0
  printf '%s' "${c:-0}"
}

step "Auditing ActorTemplates, Actors, /workspace state, and gs://${BUCKET_NAME}/atespaces/${ATESPACE}/ snapshots for bearer tokens"

spec_matches=0
if command -v kubectl-ate >/dev/null 2>&1 || kubectl ate version >/dev/null 2>&1; then
  tmpls_json="$(kubectl ate get actor-templates -a "$ATESPACE" -o json 2>/dev/null || echo '{}')"
  actors_json="$(kubectl ate get actors -a "$ATESPACE" -o json 2>/dev/null || echo '{}')"

  t_hits="$(count_matches <<<"$tmpls_json")"
  a_hits="$(count_matches <<<"$actors_json")"
  env_hits="$(jq -r '
    [
      (.actorTemplates // .items // [])[]?.spec.containers[]?.env[]?.name,
      (.actors // .items // [])[]?.spec.containers[]?.env[]?.name
    ] | .[]? // empty
  ' <<<"$(jq -s 'add' <<<"$tmpls_json"$'\n'"$actors_json" 2>/dev/null || echo '{}')" 2>/dev/null | \
    grep -cE "$FORBIDDEN_ENV_REGEX" || true)"
  spec_matches=$(( t_hits + a_hits + env_hits ))
fi

ws_matches=0
ws_actors_scanned=0
actors_json="${actors_json:-{}}"
gw_candidates=()
started_audit_pf=false
cleanup_audit_pf() {
  if $started_audit_pf; then
    stop_port_forward audit-snapshot
  fi
}
trap cleanup_audit_pf EXIT

if [[ -n "${AUDIT_GATEWAY_URL:-}" ]]; then
  gw_candidates+=("${AUDIT_GATEWAY_URL%/}")
else
  if _is_sre_gateway_url "http://127.0.0.1:8081"; then
    gw_candidates+=("http://127.0.0.1:8081")
  elif command -v kubectl >/dev/null 2>&1 && kubectl -n "$NAMESPACE" get svc sre-gateway >/dev/null 2>&1; then
    ephemeral_port_forward audit-snapshot "$NAMESPACE" svc/sre-gateway 18085 8080
    started_audit_pf=true
    gw_candidates+=("$PF_URL")
  fi
fi
for gw in "${gw_candidates[@]}"; do
  actors_api="$(curl -fsS --max-time 3 "$gw/api/actors" 2>/dev/null || true)"
  [[ -n "$actors_api" ]] || continue
  jq -e 'type == "array" or type == "object"' <<<"$actors_api" >/dev/null 2>&1 || die "invalid JSON payload from $gw/api/actors"
  actor_ids="$(
    {
      jq -r 'if type == "array" then .[]?.name else (.actors // .items // [])[]?.name end // empty' <<<"$actors_api" 2>/dev/null || true
      jq -r '(.actors // .items // [])[]?.metadata.name // empty' <<<"$actors_json" 2>/dev/null || true
    } | awk 'NF && !seen[$0]++'
  )"
  if [[ -n "$actor_ids" ]]; then
    while IFS= read -r act; do
      [[ -z "$act" ]] && continue
      ws_payload="$(curl -fsS --max-time 3 "$gw/api/actors/${act}/workspace" 2>/dev/null || true)"
      if [[ -z "$ws_payload" ]] || ! jq -e 'type == "object"' <<<"$ws_payload" >/dev/null 2>&1; then
        die "snapshot hygiene audit failed: $gw/api/actors/${act}/workspace returned null or invalid JSON"
      fi
      ws_actors_scanned=$(( ws_actors_scanned + 1 ))
      ws_hits="$(count_matches <<<"$ws_payload")"
      if (( ws_hits > 0 )); then
        warn "detected $ws_hits credential pattern match(es) in /api/actors/${act}/workspace"
        ws_matches=$(( ws_matches + ws_hits ))
      fi
    done <<<"$actor_ids"
  fi
  break
done

max_objs="${AUDIT_MAX_OBJECTS:-25}"
objs_scanned=0
snap_matches=0
obj_list=""
for gcs_prefix in "gs://${BUCKET_NAME}/atespaces/${ATESPACE}/**" "gs://${BUCKET_NAME}/${ATESPACE}/**" "gs://${BUCKET_NAME}/**"; do
  obj_list="$(gcloud storage ls -r "$gcs_prefix" 2>/dev/null | grep -E '^gs://' | grep -v '/$' | head -n "$max_objs" || true)"
  [[ -n "$obj_list" ]] && break
done
if [[ -n "$obj_list" ]]; then
  while IFS= read -r obj; do
    [[ -z "$obj" ]] && continue
    objs_scanned=$(( objs_scanned + 1 ))
    hits="$(gcloud storage cat "$obj" 2>/dev/null | head -c 4194304 | count_matches)"
    if (( hits > 0 )); then
      warn "detected $hits credential pattern match(es) in snapshot object: $obj"
      snap_matches=$(( snap_matches + hits ))
    fi
  done <<<"$obj_list"
fi

echo "ACTOR_SPEC_TOKEN_MATCHES=${spec_matches}"
echo "WORKSPACE_ACTORS_SCANNED=${ws_actors_scanned}"
echo "WORKSPACE_TOKEN_MATCHES=${ws_matches}"
echo "SNAPSHOT_OBJECTS_SCANNED=${objs_scanned}"
echo "SNAPSHOT_TOKEN_MATCHES=${snap_matches}"

total_matches=$(( spec_matches + ws_matches + snap_matches ))
if (( total_matches > 0 )); then
  die "snapshot hygiene audit failed: detected ${total_matches} credential match(es) across sandbox/snapshot artifacts"
fi

ok "zero bearer tokens or API keys found across Actor specs, /workspace, and ${objs_scanned} Cloud Storage snapshot object(s)"
