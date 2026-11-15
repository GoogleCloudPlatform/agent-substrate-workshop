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

# Provisions Module 05 zero-trust governance across the Actor Boundary:
#   1. Regional Cloud Model Armor template (sre-triage-guard)
#   2. Declarative ingress HTTPRoute + GCPAgentGateway + GCPAuthzPolicy
#   3. Agent Identity Auth Manager 3LO provider / OAuth 2.0 + S256 PKCE secrets
#   4. 3LO enforcement on ops-hub and sre-gateway (running against sre-agent-v4, zero agent rebuilds)
# shellcheck source=scripts/lib.sh
source "$(dirname "$0")/lib.sh"
load_env
resolve_spiffe_identity

PROJECT_NUMBER="${PROJECT_NUMBER:-$(state_get project-number || true)}"
if [[ -z "$PROJECT_NUMBER" ]]; then
  PROJECT_NUMBER="$(gcloud projects describe "$PROJECT_ID" --format='value(projectNumber)' 2>/dev/null || true)"
  [[ -n "$PROJECT_NUMBER" ]] && state_put project-number "$PROJECT_NUMBER"
fi

MODEL_ARMOR_TEMPLATE_NAME="${MODEL_ARMOR_TEMPLATE_NAME:-sre-triage-guard}"
TEMPLATE_JSON="$REPO_ROOT/modules/05-zero-trust-governance/manifests/model-armor-template.json"

step "1/4 Provisioning regional Cloud Model Armor template ($MODEL_ARMOR_TEMPLATE_NAME in $REGION)"
ma_ready=false
if gcloud model-armor templates describe "$MODEL_ARMOR_TEMPLATE_NAME" --location="$REGION" --project="$PROJECT_ID" >/dev/null 2>&1; then
  gcloud model-armor templates update "$MODEL_ARMOR_TEMPLATE_NAME" \
    --location="$REGION" --project="$PROJECT_ID" \
    --pi-and-jailbreak-filter-settings-enforcement=enabled \
    --pi-and-jailbreak-filter-settings-confidence-level=low-and-above \
    --malicious-uri-filter-settings-enforcement=enabled \
    --basic-config-filter-enforcement=enabled >/dev/null 2>&1 || true
  ma_ready=true
elif gcloud model-armor templates create "$MODEL_ARMOR_TEMPLATE_NAME" \
  --location="$REGION" --project="$PROJECT_ID" \
  --pi-and-jailbreak-filter-settings-enforcement=enabled \
  --pi-and-jailbreak-filter-settings-confidence-level=low-and-above \
  --malicious-uri-filter-settings-enforcement=enabled \
  --basic-config-filter-enforcement=enabled >/dev/null 2>&1; then
  ma_ready=true
else
  tok="$(gcloud auth print-access-token 2>/dev/null || true)"
  if [[ -n "$tok" && -f "$TEMPLATE_JSON" ]]; then
    ma_api="https://modelarmor.${REGION}.rep.googleapis.com/v1/projects/${PROJECT_ID}/locations/${REGION}/templates"
    http_code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
      -H "Authorization: Bearer $tok" \
      -H "Content-Type: application/json" \
      --data-binary @"$TEMPLATE_JSON" \
      "${ma_api}?templateId=${MODEL_ARMOR_TEMPLATE_NAME}" 2>/dev/null || echo "000")"
    if [[ "$http_code" == "200" || "$http_code" == "201" ]]; then
      ma_ready=true
    elif [[ "$http_code" == "409" ]]; then
      curl -fsS -X PATCH \
        -H "Authorization: Bearer $tok" \
        -H "Content-Type: application/json" \
        --data-binary @"$TEMPLATE_JSON" \
        "${ma_api}/${MODEL_ARMOR_TEMPLATE_NAME}" >/dev/null 2>&1 || true
      ma_ready=true
    fi
  fi
fi

if $ma_ready; then
  state_put model-armor-template "$MODEL_ARMOR_TEMPLATE_NAME"
  ok "Model Armor template ready: projects/${PROJECT_ID}/locations/${REGION}/templates/${MODEL_ARMOR_TEMPLATE_NAME}"
else
  state_put model-armor-template "${MODEL_ARMOR_TEMPLATE_ID:-none}"
  warn "Cloud Model Armor API unavailable in $REGION; sre-gateway deterministic prompt-injection and SDP guard remains active"
fi

step "2/4 Applying ingress HTTPRoute, GCPAgentGateway, and L7 GCPAuthzPolicy"
rendered_ingress="$("$REPO_ROOT/scripts/render.sh" "$REPO_ROOT/modules/05-zero-trust-governance/manifests/ingress-policy.yaml.tmpl")"
if kubectl get crd httproutes.gateway.networking.k8s.io >/dev/null 2>&1 &&
   kubectl get crd gcpagentgateways.networking.gke.io >/dev/null 2>&1 &&
   kubectl get crd gcpauthzpolicies.networking.gke.io >/dev/null 2>&1; then
  kubectl apply -f - <<<"$rendered_ingress"
  ok "applied GCPAgentGateway/sre-client-to-agent-gateway, HTTPRoute/sre-ingress-route, and GCPAuthzPolicy/sre-ingress-authz-policy"
elif kubectl get crd httproutes.gateway.networking.k8s.io >/dev/null 2>&1; then
  extract_ingress_httproute <<<"$rendered_ingress" | kubectl apply -f - || true
  warn "applied HTTPRoute/sre-ingress-route (GKE Enterprise GCPAgentGateway/GCPAuthzPolicy CRDs not installed on cluster)"
else
  warn "Gateway API CRDs not installed on cluster; skipping ingress-policy.yaml.tmpl apply"
fi

GATEWAY_IP="$(kubectl -n "$NAMESPACE" get gateway sre-ingress-gateway -o jsonpath='{.status.addresses[0].value}' 2>/dev/null || true)"
if [[ -n "$GATEWAY_IP" ]]; then
  state_put gateway-ip "$GATEWAY_IP"
  ok "regional Gateway/sre-ingress-gateway VIP: http://${GATEWAY_IP} (GKE regional Envoy takes ~2-4 min to finish propagating new HTTPRoute/GCPAuthzPolicy rules)"
fi

step "3/4 Configuring Agent Identity Auth Manager and OAuth 2.0 + S256 PKCE credentials"
secret_file="$STATE_DIR/oauth-client-secret"
if [[ ! -s "$secret_file" ]]; then
  (
    umask 077
    if command -v openssl >/dev/null 2>&1; then
      openssl rand -hex 32 >"$secret_file"
    else
      od -An -N32 -tx1 /dev/urandom | tr -d ' \n' >"$secret_file"
      printf '\n' >>"$secret_file"
    fi
  )
fi
chmod 600 "$secret_file"

kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl create namespace "$SIM_NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

kubectl -n "$SIM_NAMESPACE" create secret generic ops-hub-oauth \
  --from-file=client-secret="$secret_file" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

sec_tmp="$(mktemp -d "$STATE_DIR/oauth-sec.XXXXXX")"
chmod 700 "$sec_tmp"
cp "$secret_file" "$sec_tmp/client-secret"
sec_args=(--from-file=client-secret="$sec_tmp/client-secret")
if [[ -n "${GITHUB_OAUTH_CLIENT_ID:-}" ]]; then
  printf '%s' "$GITHUB_OAUTH_CLIENT_ID" >"$sec_tmp/github-client-id"
  sec_args+=(--from-file=github-client-id="$sec_tmp/github-client-id")
fi
if [[ -n "${GITHUB_OAUTH_CLIENT_SECRET:-}" ]]; then
  printf '%s' "$GITHUB_OAUTH_CLIENT_SECRET" >"$sec_tmp/github-client-secret"
  sec_args+=(--from-file=github-client-secret="$sec_tmp/github-client-secret")
fi
kubectl -n "$NAMESPACE" create secret generic sre-gateway-oauth \
  "${sec_args[@]}" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
rm -rf "$sec_tmp"

if [[ -n "${GITHUB_OAUTH_CLIENT_ID:-}" || "${AUTH_PROVIDER_HTTPS_URL:-}" == https://* ]]; then
  if [[ -n "${GITHUB_OAUTH_CLIENT_ID:-}" ]]; then
    ap_id="github-3lo"
    ap_auth_url="https://github.com/login/oauth/authorize"
    ap_token_url="https://github.com/login/oauth/access_token"
    ap_client_id="$GITHUB_OAUTH_CLIENT_ID"
    ap_client_secret="${GITHUB_OAUTH_CLIENT_SECRET:-$(cat "$secret_file")}"
    ap_continue="${AUTH_PROVIDER_CONTINUE_URI:-https://${NAMESPACE}-sre-gateway.example.com/api/oauth/callback}"
    ap_scopes="repo"
  else
    ap_base="${AUTH_PROVIDER_HTTPS_URL%/}"
    ap_id="ops-hub-3lo"
    ap_auth_url="${ap_base}/oauth/authorize"
    ap_token_url="${ap_base}/oauth/token"
    ap_client_id="${OAUTH_CLIENT_ID:-sre-gateway-3lo}"
    ap_client_secret="$(cat "$secret_file")"
    ap_continue="${AUTH_PROVIDER_CONTINUE_URI:-${ap_base}/api/oauth/callback}"
    ap_scopes="openid,profile,remediation:write"
  fi

  ap_registered=false
  if gcloud agent-identity auth-providers describe "$ap_id" --location="$REGION" --project="$PROJECT_ID" >/dev/null 2>&1; then
    if gcloud agent-identity auth-providers update "$ap_id" \
      --location="$REGION" --project="$PROJECT_ID" \
      --three-legged-oauth-authorization-url="$ap_auth_url" \
      --three-legged-oauth-token-url="$ap_token_url" \
      --three-legged-oauth-client-id="$ap_client_id" \
      --three-legged-oauth-client-secret="$ap_client_secret" \
      --three-legged-oauth-default-continue-uri="$ap_continue" \
      --three-legged-oauth-enable-pkce \
      --allowed-scopes="$ap_scopes" >/dev/null 2>&1; then
      ap_registered=true
    fi
  elif gcloud agent-identity auth-providers create "$ap_id" \
    --location="$REGION" --project="$PROJECT_ID" \
    --three-legged-oauth-authorization-url="$ap_auth_url" \
    --three-legged-oauth-token-url="$ap_token_url" \
    --three-legged-oauth-client-id="$ap_client_id" \
    --three-legged-oauth-client-secret="$ap_client_secret" \
    --three-legged-oauth-default-continue-uri="$ap_continue" \
    --three-legged-oauth-enable-pkce \
    --allowed-scopes="$ap_scopes" >/dev/null 2>&1; then
    ap_registered=true
  fi

  if $ap_registered && [[ -n "$PROJECT_NUMBER" ]]; then
    ap_resource="projects/${PROJECT_NUMBER}/locations/${REGION}/authProviders/${ap_id}"
    state_put auth-provider-id "$ap_resource"
    ok "registered Cloud Agent Identity AuthProvider: $ap_resource"
  else
    state_put auth-provider-id "${GCP_AUTH_PROVIDER_ID:-none}"
    warn "Cloud Agent Identity AuthProvider registration unavailable; using sre-gateway OAuth 2.0 + S256 PKCE vault"
  fi
else
  gcloud agent-identity auth-providers list --location="$REGION" --project="$PROJECT_ID" >/dev/null 2>&1 || true
  state_put auth-provider-id "${GCP_AUTH_PROVIDER_ID:-none}"
  ok "configured self-contained Operations Hub OAuth 2.0 + S256 PKCE vault (Secret/ops-hub-oauth & Secret/sre-gateway-oauth)"
fi

step "4/4 Enabling 3LO remediation enforcement on ops-hub and sre-gateway (template sre-agent-v4)"
ops_env_args=(OPS_HUB_REQUIRE_3LO=true)
if [[ -n "$GATEWAY_IP" ]]; then
  ops_env_args+=("PUBLIC_BASE_URL=http://${GATEWAY_IP}")
fi
kubectl -n "$SIM_NAMESPACE" set env deploy/ops-hub "${ops_env_args[@]}" >/dev/null
kubectl -n "$SIM_NAMESPACE" rollout status deploy/ops-hub --timeout=120s

state_put governance-enforced "true"
state_put require-3lo "true"

TARGET_TEMPLATE="${WORKSHOP_TEMPLATE_OVERRIDE:-sre-agent-v4}"
WORKSHOP_TEMPLATE_OVERRIDE="$TARGET_TEMPLATE" \
  DEFAULT_ACTOR_TEMPLATE="$TARGET_TEMPLATE" \
  WORKSHOP_GOV_OVERRIDE=true \
  GOVERNANCE_ENFORCED=true \
  WORKSHOP_3LO_OVERRIDE=true \
  REQUIRE_3LO_FOR_REMEDIATION=true \
  "$REPO_ROOT/scripts/sre-gateway.sh"

ok "Module 05 zero-trust governance active on sre-gateway (ActorTemplate=$TARGET_TEMPLATE, zero agent image rebuilds)"
