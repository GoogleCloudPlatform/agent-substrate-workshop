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

# render.sh <file.tmpl>: print the template with ${VAR} filled from workshop.env, SPIFFE identity
# auto-detection (org-* -> proj-* -> WIF fallback), and .workshop/ state files.
source "$(dirname "$0")/lib.sh"
load_env
resolve_spiffe_identity

tmpl="${1:?usage: render.sh <file.tmpl>}"
[[ -f "$tmpl" ]] || die "no such template: $tmpl"

if [[ -f "$STATE_DIR/agent-image-features" ]]; then
  built_features="$(state_get agent-image-features || true)"
  req_feature=""
  req_mod=""
  case "${tmpl##*/}" in
    template-v1.yaml.tmpl) req_feature="NewModel"; req_mod="WORKSHOP(module-1)" ;;
    template-v2.yaml.tmpl) req_feature="NewMemoryService"; req_mod="WORKSHOP(module-2)" ;;
    template-v3.yaml.tmpl) req_feature="NewSkillSource"; req_mod="WORKSHOP(module-3)" ;;
    template-v4.yaml.tmpl) req_feature="DiscoverMCPToolsets"; req_mod="WORKSHOP(module-4)" ;;
  esac
  if [[ -n "$req_feature" && "$built_features" != *"$req_feature"* ]]; then
    die "${tmpl##*/} requires ${req_feature} (${req_mod}) in app/cmd/agent/main.go, which was missing from your last 'make build-agent'. Save app/cmd/agent/main.go in Cloud Shell Editor (Ctrl+S / Cmd+S), run 'make build-agent', and re-run this command."
  fi
fi

state_source() {
  case "$1" in
    AGENT_IMAGE)         echo "agent-image|make build-agent" ;;
    GATEWAY_IMAGE)       echo "gateway-image|make gateway" ;;
    WORKER_IMAGE)        echo "worker-image|make install" ;;
    LEDGER_API_IMAGE)    echo "ledger-api-image|make prod-sim" ;;
    OPS_HUB_IMAGE)       echo "ops-hub-image|make prod-sim" ;;
    PROJECT_NUMBER)      echo "project-number|make gcp" ;;
    REASONING_ENGINE_ID) echo "reasoning-engine-id|make gcp" ;;
    *) return 1 ;;
  esac
}

vars=()
while IFS= read -r v; do vars+=("$v"); done < <(grep -oE '\$\{[A-Z_][A-Z0-9_]*\}' "$tmpl" | tr -d '${}' | sort -u)
if (( ${#vars[@]} > 0 )); then
  for v in "${vars[@]}"; do
    case "$v" in
      REASONING_ENGINE_ID)
        if [[ -z "${REASONING_ENGINE_ID:-}" ]]; then
          REASONING_ENGINE_ID="$(state_get reasoning-engine-id || true)"
          : "${REASONING_ENGINE_ID:=broker-durable}"
          export REASONING_ENGINE_ID
        fi
        continue ;;
      GOVERNANCE_ENFORCED)
        if [[ -z "${GOVERNANCE_ENFORCED:-}" ]]; then
          GOVERNANCE_ENFORCED="$(state_get governance-enforced || true)"
          : "${GOVERNANCE_ENFORCED:=false}"
          export GOVERNANCE_ENFORCED
        fi
        continue ;;
      REQUIRE_3LO_FOR_REMEDIATION)
        if [[ -z "${REQUIRE_3LO_FOR_REMEDIATION:-}" ]]; then
          REQUIRE_3LO_FOR_REMEDIATION="$(state_get require-3lo || true)"
          : "${REQUIRE_3LO_FOR_REMEDIATION:=false}"
          export REQUIRE_3LO_FOR_REMEDIATION
        fi
        continue ;;
      MODEL_ARMOR_TEMPLATE_ID)
        if [[ -z "${MODEL_ARMOR_TEMPLATE_ID:-}" ]]; then
          MODEL_ARMOR_TEMPLATE_ID="$(state_get model-armor-template || true)"
          : "${MODEL_ARMOR_TEMPLATE_ID:=none}"
          export MODEL_ARMOR_TEMPLATE_ID
        fi
        continue ;;
      GCP_AUTH_PROVIDER_ID)
        if [[ -z "${GCP_AUTH_PROVIDER_ID:-}" ]]; then
          GCP_AUTH_PROVIDER_ID="$(state_get auth-provider-id || true)"
          : "${GCP_AUTH_PROVIDER_ID:=none}"
          export GCP_AUTH_PROVIDER_ID
        fi
        continue ;;
    esac
    if [[ -z "${!v:-}" ]] && src="$(state_source "$v")"; then
      require_state "${src%%|*}" "${src#*|}"
      printf -v "$v" '%s' "$(state_get "${src%%|*}")"
      export "${v?}"
    fi
  done
  render "$tmpl" "${vars[@]}"
else
  cat "$tmpl"
fi
