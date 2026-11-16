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

# Renders and applies sre-gateway (the Governed Broker and Incident Console),
# preserving any live IDLE_AFTER or MOCK_LLM overrides unless explicitly set.
source "$(dirname "$0")/lib.sh"
load_env
resolve_spiffe_identity

if [[ -z "${GATEWAY_IMAGE:-}" ]]; then
  if [[ -s "$STATE_DIR/gateway-image-built" ]]; then
    GATEWAY_IMAGE="$(state_get gateway-image-built)"
    state_put gateway-image "$GATEWAY_IMAGE"
    rm -f "$STATE_DIR/gateway-image-built"
  else
    live_gw_img="$(kubectl -n "$NAMESPACE" get deploy sre-gateway \
      -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || true)"
    if [[ -n "$live_gw_img" ]]; then
      GATEWAY_IMAGE="$live_gw_img"
      state_put gateway-image "$GATEWAY_IMAGE"
    else
      if [[ ! -s "$STATE_DIR/gateway-image" ]]; then
        "$REPO_ROOT/scripts/build.sh" sre-gateway
      fi
      GATEWAY_IMAGE="$(state_get gateway-image)"
    fi
  fi
fi
export GATEWAY_IMAGE

if [[ -z "${IDLE_AFTER:-}" ]]; then
  live_idle="$(kubectl -n "$NAMESPACE" get deploy sre-gateway \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="IDLE_AFTER")].value}' 2>/dev/null || true)"
  IDLE_AFTER="${live_idle:-15s}"
fi
export IDLE_AFTER

if [[ -z "${WORKSHOP_MOCK_LLM_OVERRIDE:-}" ]]; then
  live_mock="$(kubectl -n "$NAMESPACE" get deploy sre-gateway \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="MOCK_LLM")].value}' 2>/dev/null || true)"
  if [[ -n "$live_mock" && "$live_mock" != "$MOCK_LLM" ]]; then
    warn "preserving live MOCK_LLM=$live_mock on deploy/sre-gateway (pass MOCK_LLM=$MOCK_LLM make gateway to override)"
    MOCK_LLM="$live_mock"
  fi
fi
export MOCK_LLM

if [[ -z "${WORKSHOP_TEMPLATE_OVERRIDE:-}" ]]; then
  live_tmpl="$(kubectl -n "$NAMESPACE" get deploy sre-gateway \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="DEFAULT_ACTOR_TEMPLATE")].value}' 2>/dev/null || true)"
  if [[ -n "$live_tmpl" ]]; then
    DEFAULT_ACTOR_TEMPLATE="$live_tmpl"
  fi
fi
export DEFAULT_ACTOR_TEMPLATE

if [[ -z "${REASONING_ENGINE_ID:-}" ]]; then
  if [[ -s "$STATE_DIR/reasoning-engine-id" ]]; then
    REASONING_ENGINE_ID="$(state_get reasoning-engine-id)"
  else
    live_re="$(kubectl -n "$NAMESPACE" get deploy sre-gateway \
      -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="REASONING_ENGINE_ID")].value}' 2>/dev/null || true)"
    REASONING_ENGINE_ID="${live_re:-broker-durable}"
  fi
fi
export REASONING_ENGINE_ID

if [[ -z "${WORKSHOP_GOV_OVERRIDE:-}" ]]; then
  if [[ -s "$STATE_DIR/governance-enforced" ]]; then
    GOVERNANCE_ENFORCED="$(state_get governance-enforced)"
  else
    live_gov="$(kubectl -n "$NAMESPACE" get deploy sre-gateway \
      -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="GOVERNANCE_ENFORCED")].value}' 2>/dev/null || true)"
    GOVERNANCE_ENFORCED="${live_gov:-false}"
  fi
fi
export GOVERNANCE_ENFORCED

if [[ -z "${WORKSHOP_3LO_OVERRIDE:-}" ]]; then
  if [[ -s "$STATE_DIR/require-3lo" ]]; then
    REQUIRE_3LO_FOR_REMEDIATION="$(state_get require-3lo)"
  else
    live_3lo="$(kubectl -n "$NAMESPACE" get deploy sre-gateway \
      -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="REQUIRE_3LO_FOR_REMEDIATION")].value}' 2>/dev/null || true)"
    REQUIRE_3LO_FOR_REMEDIATION="${live_3lo:-false}"
  fi
fi
export REQUIRE_3LO_FOR_REMEDIATION

if [[ -z "${MODEL_ARMOR_TEMPLATE_ID:-}" ]]; then
  if [[ -s "$STATE_DIR/model-armor-template" ]]; then
    MODEL_ARMOR_TEMPLATE_ID="$(state_get model-armor-template)"
  else
    live_ma="$(kubectl -n "$NAMESPACE" get deploy sre-gateway \
      -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="MODEL_ARMOR_TEMPLATE_ID")].value}' 2>/dev/null || true)"
    MODEL_ARMOR_TEMPLATE_ID="${live_ma:-none}"
  fi
fi
export MODEL_ARMOR_TEMPLATE_ID

if [[ -z "${GCP_AUTH_PROVIDER_ID:-}" ]]; then
  if [[ -s "$STATE_DIR/auth-provider-id" ]]; then
    GCP_AUTH_PROVIDER_ID="$(state_get auth-provider-id)"
  else
    live_ap="$(kubectl -n "$NAMESPACE" get deploy sre-gateway \
      -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="GCP_AUTH_PROVIDER_ID")].value}' 2>/dev/null || true)"
    GCP_AUTH_PROVIDER_ID="${live_ap:-none}"
  fi
fi
export GCP_AUTH_PROVIDER_ID

kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl label namespace "$NAMESPACE" ate.dev/api-access=true ate.dev/gateway-access=true --overwrite >/dev/null
ensure_atespace "$ATESPACE"

step "Deploying sre-gateway ($GATEWAY_IMAGE, TEMPLATE=$DEFAULT_ACTOR_TEMPLATE, MOCK_LLM=$MOCK_LLM, IDLE_AFTER=$IDLE_AFTER, SPIFFE=$AGENT_IDENTITY_MODE, GOV=$GOVERNANCE_ENFORCED, 3LO=$REQUIRE_3LO_FOR_REMEDIATION)"
"$REPO_ROOT/scripts/render.sh" "$REPO_ROOT/modules/01-actor-boundary/manifests/sre-gateway.yaml.tmpl" | kubectl apply -f -
kubectl -n "$NAMESPACE" rollout status deploy/sre-gateway --timeout=180s

# Pre-stage HTTPRoute/sre-ingress-route when Gateway/sre-ingress-gateway already exists so GKE's
# regional external load balancer programs its BackendService, HealthCheck, and URLMap in the
# background well before Module 05 attaches GCPAuthzPolicy/sre-ingress-authz-policy.
if kubectl get crd httproutes.gateway.networking.k8s.io >/dev/null 2>&1 &&
   kubectl -n "$NAMESPACE" get gateway sre-ingress-gateway >/dev/null 2>&1; then
  "$REPO_ROOT/scripts/render.sh" "$REPO_ROOT/modules/05-zero-trust-governance/manifests/ingress-policy.yaml.tmpl" | \
    extract_ingress_httproute | kubectl apply -f - >/dev/null 2>&1 || true
fi

if [[ -f "$STATE_DIR/pf-gateway.pid" ]]; then
  stop_port_forward gateway
  ok "stopped stale gateway port-forward after rollout; run make forward-gateway to reconnect"
fi

# Re-synchronize any previously published Procedural Skill archives from .workshop/skills/
# so active defaultRevision (e.g., REV=v2) survives pod rollouts even on fresh storage.
resync_staged_skills() {
  local active_files=() f base skill_name active_rev zip_file rev sync_url="${BROKER_SYNC_URL:-}" started_pf=false
  for f in "$STATE_DIR"/skill-*-active-rev; do
    [[ -s "$f" ]] && active_files+=("$f")
  done
  (( ${#active_files[@]} > 0 )) || return 0

  if [[ -z "$sync_url" ]]; then
    if _is_sre_gateway_url "http://127.0.0.1:8081"; then
      sync_url="http://127.0.0.1:8081"
    elif command -v kubectl >/dev/null 2>&1 && kubectl -n "$NAMESPACE" get svc sre-gateway >/dev/null 2>&1; then
      if ephemeral_port_forward gateway-skill-sync "$NAMESPACE" svc/sre-gateway 18085 8080 >/dev/null 2>&1; then
        started_pf=true
        sync_url="$PF_URL"
      fi
    fi
  fi
  if [[ -z "$sync_url" ]]; then
    return 0
  fi

  for f in "${active_files[@]}"; do
    base="${f##*/skill-}"
    skill_name="${base%-active-rev}"
    active_rev="$(cat "$f")"
    for zip_file in "$STATE_DIR/skills/${skill_name}-"*.zip; do
      [[ -f "$zip_file" ]] || continue
      local zname="${zip_file##*/}"
      rev="${zname#"${skill_name}-"}"
      rev="${rev%.zip}"
      [[ "$rev" == "$active_rev" ]] && continue
      SKILL="$skill_name" REV="$rev" SET_DEFAULT=false SKIP_GCLOUD_SKILL_REGISTRY=1 BROKER_SYNC_URL="$sync_url" \
        "$REPO_ROOT/scripts/publish-skill.sh" >/dev/null 2>&1 || true
    done
    if [[ -n "$active_rev" && -f "$STATE_DIR/skills/${skill_name}-${active_rev}.zip" ]]; then
      SKILL="$skill_name" REV="$active_rev" SET_DEFAULT=true SKIP_GCLOUD_SKILL_REGISTRY=1 BROKER_SYNC_URL="$sync_url" \
        "$REPO_ROOT/scripts/publish-skill.sh" >/dev/null 2>&1 || warn "could not re-sync ${skill_name}@${active_rev} after sre-gateway rollout"
    fi
  done

  if $started_pf; then
    stop_port_forward gateway-skill-sync
  fi
}
resync_staged_skills

ok "sre-gateway ready: http://sre-gateway.${NAMESPACE}.svc.cluster.local:8080"
