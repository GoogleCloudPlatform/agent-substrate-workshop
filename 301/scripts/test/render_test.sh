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

# Offline tests for scripts/render.sh: state-backed variables, Module 00 manifests, and SPIFFE identity modes.
# shellcheck disable=SC2034 # $out is read inside check() eval strings
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
export WORKSHOP_ENV_FILE="$TMP/env" WORKSHOP_STATE_DIR="$TMP/ws"
cat >"$WORKSHOP_ENV_FILE" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_NAME=ate-cluster
CLUSTER_LOCATION=us-central1-a
BUCKET_NAME=${PROJECT_ID}-ate
EOF
mkdir -p "$WORKSHOP_STATE_DIR"
fails=0
check() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; fails=$((fails+1)); fi; }

printf 'image: ${AGENT_IMAGE}\nbucket: gs://${BUCKET_NAME}/${ATESPACE}/\nkeep: $HOME\n' >"$TMP/t.yaml.tmpl"
echo "r/agent@sha256:abc" >"$WORKSHOP_STATE_DIR/agent-image"
out="$(bash "$ROOT/scripts/render.sh" "$TMP/t.yaml.tmpl" 2>&1)"
check "fills state-backed vars" "[[ \"\$out\" == *'image: r/agent@sha256:abc'* ]]"
check "fills 301 env defaults" "[[ \"\$out\" == *'bucket: gs://demo-proj-ate/workshop-301/'* ]]"
check "leaves non-template text alone" "[[ \"\$out\" == *'keep: \$HOME'* ]]"

rm "$WORKSHOP_STATE_DIR/agent-image"
out="$(bash "$ROOT/scripts/render.sh" "$TMP/t.yaml.tmpl" 2>&1)"; rc=$?
check "missing state fails" "[[ $rc -ne 0 ]]"
check "missing state names the make target" "[[ \"\$out\" == *'make build-agent'* ]]"

# Test SPIFFE Agent Identity resolution: org-* -> proj-* -> standard WIF fallback.
cat >"$TMP/spiffe.tmpl" <<'EOF'
mode: ${AGENT_IDENTITY_MODE}
trustDomain: ${SPIFFE_TRUST_DOMAIN}
identityType: ${SPIFFE_IDENTITY_TYPE}
injectCerts: ${SPIFFE_INJECT_PODCERTIFICATES}
idPattern: ${SPIFFE_ID_PATTERN}
EOF

echo "11223344" >"$WORKSHOP_STATE_DIR/project-number"
echo "99887766" >"$WORKSHOP_STATE_DIR/org-id"
out="$(bash "$ROOT/scripts/render.sh" "$TMP/spiffe.tmpl" 2>&1)"
check "org-scoped SPIFFE trust domain takes precedence" \
  "[[ \"\$out\" == *'mode: org'* && \"\$out\" == *'trustDomain: agents.global.org-99887766.system.id.goog'* && \"\$out\" == *'injectCerts: true'* ]]"

rm -f "$WORKSHOP_STATE_DIR/org-id"
echo "true" >"$WORKSHOP_STATE_DIR/agent-identity-enabled"
out="$(bash "$ROOT/scripts/render.sh" "$TMP/spiffe.tmpl" 2>&1)"
check "project-scoped SPIFFE trust domain resolves when org-id absent" \
  "[[ \"\$out\" == *'mode: project'* && \"\$out\" == *'trustDomain: agents.global.proj-11223344.system.id.goog'* && \"\$out\" == *'identityType: agent-identity'* ]]"

rm -f "$WORKSHOP_STATE_DIR/agent-identity-enabled"
out="$(bash "$ROOT/scripts/render.sh" "$TMP/spiffe.tmpl" 2>&1)"
check "standard Workload Identity fallback resolves when Agent Identity not active" \
  "[[ \"\$out\" == *'mode: wif'* && \"\$out\" == *'trustDomain: demo-proj.svc.id.goog'* && \"\$out\" == *'injectCerts: false'* ]]"

# Render all Module 00 manifest templates and verify zero unresolved placeholders.
echo "r/ledger@sha256:111" >"$WORKSHOP_STATE_DIR/ledger-api-image"
echo "r/opshub@sha256:222" >"$WORKSHOP_STATE_DIR/ops-hub-image"

cc_default="$(bash "$ROOT/scripts/render.sh" "$ROOT/modules/00-setup/manifests/computeclass.yaml.tmpl" 2>&1)"
check "computeclass.yaml.tmpl disables nodePoolAutoCreation by default and sets 30m consolidation" \
  "[[ \"\$cc_default\" == *'enabled: false'* && \"\$cc_default\" == *'consolidationDelayMinutes: 30'* && \"\$cc_default\" == *'machineFamily: c3'* && \"\$cc_default\" == *'machineFamily: n4'* && \"\$cc_default\" == *'machineFamily: n2'* && \"\$cc_default\" != *'\${'* ]]"

cc_optin="$(ENABLE_COMPUTE_CLASS_AUTOPROVISION=true bash "$ROOT/scripts/render.sh" "$ROOT/modules/00-setup/manifests/computeclass.yaml.tmpl" 2>&1)"
check "computeclass.yaml.tmpl enables nodePoolAutoCreation when opted in" \
  "[[ \"\$cc_optin\" == *'enabled: true'* ]]"

gw_out="$(bash "$ROOT/scripts/render.sh" "$ROOT/modules/00-setup/manifests/ingress-gateway.yaml.tmpl" 2>&1)"
check "ingress-gateway.yaml.tmpl renders external and agent Gateways plus GCPAgentGateway cleanly" \
  "[[ \"\$gw_out\" == *'name: sre-ingress-gateway'* && \"\$gw_out\" == *'name: sre-agent-gateway'* && \"\$gw_out\" == *'gatewayClassName: gke-l7-rilb'* && \"\$gw_out\" == *'kind: GCPAgentGateway'* && \"\$gw_out\" != *'\${'* ]]"

sim_out="$(bash "$ROOT/scripts/render.sh" "$ROOT/modules/00-setup/manifests/prod-sim.yaml.tmpl" 2>&1)"
check "prod-sim.yaml.tmpl renders 50m/64Mi pods for ledger-api and ops-hub" \
  "[[ \"\$sim_out\" == *'cpu: 50m'* && \"\$sim_out\" == *'memory: 64Mi'* && \"\$sim_out\" == *'r/ledger@sha256:111'* && \"\$sim_out\" == *'r/opshub@sha256:222'* && \"\$sim_out\" != *'\${'* ]]"

# Render all Module 01 manifest templates across org, project, and wif SPIFFE modes.
echo "r/worker@sha256:333" >"$WORKSHOP_STATE_DIR/worker-image"
echo "r/gateway@sha256:444" >"$WORKSHOP_STATE_DIR/gateway-image"
echo "r/agent@sha256:555" >"$WORKSHOP_STATE_DIR/agent-image"

wp_out="$(bash "$ROOT/scripts/render.sh" "$ROOT/modules/01-actor-boundary/manifests/workerpool.yaml.tmpl" 2>&1)"
check "workerpool.yaml.tmpl renders WorkerPool/sre-workers with 2x 250m/1Gi and api-access label" \
  "[[ \"\$wp_out\" == *'name: sre-workers'* && \"\$wp_out\" == *'replicas: 2'* && \"\$wp_out\" == *'cpu: 250m'* && \"\$wp_out\" == *'memory: 1Gi'* && \"\$wp_out\" == *'ate.dev/api-access: \"true\"'* && \"\$wp_out\" != *'\${'* ]]"

gw_wif="$(IDLE_AFTER=15s bash "$ROOT/scripts/render.sh" "$ROOT/modules/01-actor-boundary/manifests/sre-gateway.yaml.tmpl" 2>&1)"
check "sre-gateway.yaml.tmpl renders 100m/128Mi broker with ate-identity, Recreate PVC state, and WIF SPIFFE defaults" \
  "[[ \"\$gw_wif\" == *'name: sre-gateway'* && \"\$gw_wif\" == *'kind: PersistentVolumeClaim'* && \"\$gw_wif\" == *'claimName: sre-gateway-state'* && \"\$gw_wif\" == *'type: Recreate'* && \"\$gw_wif\" == *'BROKER_STATE_FILE'* && \"\$gw_wif\" == *'cpu: 100m'* && \"\$gw_wif\" == *'memory: 128Mi'* && \"\$gw_wif\" == *'credential-bundle.pem'* && \"\$gw_wif\" == *'trust-bundle.pem'* && \"\$gw_wif\" == *'demo-proj.svc.id.goog'* && \"\$gw_wif\" != *'\${'* ]]"
check "sre-gateway.yaml.tmpl carries GKE Agent Registry functional-type label and A2A agent-card annotation" \
  "[[ \"\$gw_wif\" == *'registry.gke.io/functional-type: \"AGENT\"'* && \"\$gw_wif\" == *'a2a-protocol.org/agent-card:'* && \"\$gw_wif\" == *'/.well-known/agent-card.json'* && \"\$gw_wif\" == *'8080'* ]]"

echo "99887766" >"$WORKSHOP_STATE_DIR/org-id"
gw_org="$(IDLE_AFTER=15s bash "$ROOT/scripts/render.sh" "$ROOT/modules/01-actor-boundary/manifests/sre-gateway.yaml.tmpl" 2>&1)"
check "sre-gateway.yaml.tmpl renders org-scoped Pod Agent Identity annotations when org-id exists" \
  "[[ \"\$gw_org\" == *'agents.global.org-99887766.system.id.goog'* && \"\$gw_org\" == *'iam.gke.io/inject-podcertificates: \"true\"'* && \"\$gw_org\" != *'\${'* ]]"

rm -f "$WORKSHOP_STATE_DIR/org-id"
echo "true" >"$WORKSHOP_STATE_DIR/agent-identity-enabled"
gw_proj="$(IDLE_AFTER=15s bash "$ROOT/scripts/render.sh" "$ROOT/modules/01-actor-boundary/manifests/sre-gateway.yaml.tmpl" 2>&1)"
check "sre-gateway.yaml.tmpl renders project-scoped Pod Agent Identity annotations" \
  "[[ \"\$gw_proj\" == *'agents.global.proj-11223344.system.id.goog'* && \"\$gw_proj\" == *'iam.gke.io/spiffe-identity-type: \"agent-identity\"'* && \"\$gw_proj\" != *'\${'* ]]"
rm -f "$WORKSHOP_STATE_DIR/agent-identity-enabled"

tmpl_v1="$(bash "$ROOT/scripts/render.sh" "$ROOT/modules/01-actor-boundary/manifests/template-v1.yaml.tmpl" 2>&1)"
check "template-v1.yaml.tmpl renders keyless sre-agent-v1 on port 80 with 768Mi and SNAPSHOT_FIDELITY_MEMORY" \
  "[[ \"\$tmpl_v1\" == *'name: sre-agent-v1'* && \"\$tmpl_v1\" == *'port: 80'* && \"\$tmpl_v1\" == *'NET_BIND_SERVICE'* && \"\$tmpl_v1\" == *'memoryBytes: 805306368'* && \"\$tmpl_v1\" == *'SNAPSHOT_FIDELITY_MEMORY'* && \"\$tmpl_v1\" == *'mountPath: /workspace'* && \"\$tmpl_v1\" != *'API_KEY'* && \"\$tmpl_v1\" != *'\${'* ]]"

eg_out="$(bash "$ROOT/scripts/render.sh" "$ROOT/modules/01-actor-boundary/manifests/egress-policy.yaml.tmpl" 2>&1)"
check "egress-policy.yaml.tmpl restricts egress strictly to sre-gateway in workshop-301" \
  "[[ \"\$eg_out\" == *'sre-gateway.workshop-301.svc.cluster.local'* && \"\$eg_out\" != *'\${'* ]]"

# Module 02: optional REASONING_ENGINE_ID on sre-gateway + template-v2.yaml.tmpl.
check "sre-gateway.yaml.tmpl defaults REASONING_ENGINE_ID to broker-durable when .workshop/reasoning-engine-id absent" \
  "[[ \"\$gw_wif\" == *'name: REASONING_ENGINE_ID'* && \"\$gw_wif\" == *'value: \"broker-durable\"'* ]]"

echo "9988776655" >"$WORKSHOP_STATE_DIR/reasoning-engine-id"
gw_re="$(IDLE_AFTER=15s DEFAULT_ACTOR_TEMPLATE=sre-agent-v2 bash "$ROOT/scripts/render.sh" "$ROOT/modules/01-actor-boundary/manifests/sre-gateway.yaml.tmpl" 2>&1)"
check "sre-gateway.yaml.tmpl picks up .workshop/reasoning-engine-id and DEFAULT_ACTOR_TEMPLATE=sre-agent-v2" \
  "[[ \"\$gw_re\" == *'value: \"9988776655\"'* && \"\$gw_re\" == *'value: \"sre-agent-v2\"'* && \"\$gw_re\" != *'\${'* ]]"
rm -f "$WORKSHOP_STATE_DIR/reasoning-engine-id"

tmpl_v2="$(bash "$ROOT/scripts/render.sh" "$ROOT/modules/02-sessions-and-memory/manifests/template-v2.yaml.tmpl" 2>&1)"
check "template-v2.yaml.tmpl renders keyless sre-agent-v2 on port 80 with 768Mi and SNAPSHOT_FIDELITY_MEMORY" \
  "[[ \"\$tmpl_v2\" == *'name: sre-agent-v2'* && \"\$tmpl_v2\" == *'value: \"sre-agent-v2\"'* && \"\$tmpl_v2\" == *'port: 80'* && \"\$tmpl_v2\" == *'NET_BIND_SERVICE'* && \"\$tmpl_v2\" == *'memoryBytes: 805306368'* && \"\$tmpl_v2\" == *'SNAPSHOT_FIDELITY_MEMORY'* && \"\$tmpl_v2\" != *'API_KEY'* && \"\$tmpl_v2\" != *'\${'* ]]"

tmpl_v3="$(bash "$ROOT/scripts/render.sh" "$ROOT/modules/03-skill-registry/manifests/template-v3.yaml.tmpl" 2>&1)"
check "template-v3.yaml.tmpl renders keyless sre-agent-v3 on port 80 with 768Mi and SNAPSHOT_FIDELITY_MEMORY" \
  "[[ \"\$tmpl_v3\" == *'name: sre-agent-v3'* && \"\$tmpl_v3\" == *'value: \"sre-agent-v3\"'* && \"\$tmpl_v3\" == *'port: 80'* && \"\$tmpl_v3\" == *'NET_BIND_SERVICE'* && \"\$tmpl_v3\" == *'memoryBytes: 805306368'* && \"\$tmpl_v3\" == *'SNAPSHOT_FIDELITY_MEMORY'* && \"\$tmpl_v3\" != *'API_KEY'* && \"\$tmpl_v3\" != *'\${'* ]]"

tmpl_v4="$(bash "$ROOT/scripts/render.sh" "$ROOT/modules/04-agent-registry-mcp-a2a/manifests/template-v4.yaml.tmpl" 2>&1)"
check "template-v4.yaml.tmpl renders keyless sre-agent-v4 on port 80 with 768Mi and SNAPSHOT_FIDELITY_MEMORY" \
  "[[ \"\$tmpl_v4\" == *'name: sre-agent-v4'* && \"\$tmpl_v4\" == *'value: \"sre-agent-v4\"'* && \"\$tmpl_v4\" == *'port: 80'* && \"\$tmpl_v4\" == *'NET_BIND_SERVICE'* && \"\$tmpl_v4\" == *'memoryBytes: 805306368'* && \"\$tmpl_v4\" == *'SNAPSHOT_FIDELITY_MEMORY'* && \"\$tmpl_v4\" != *'API_KEY'* && \"\$tmpl_v4\" != *'\${'* ]]"

# Module 05: sre-gateway governance env vars + ingress-policy.yaml.tmpl schema rules.
check "sre-gateway.yaml.tmpl defaults Module 05 governance vars to safe sentinels" \
  "[[ \"\$gw_wif\" == *'name: GOVERNANCE_ENFORCED'* && \"\$gw_wif\" == *'name: REQUIRE_3LO_FOR_REMEDIATION'* && \"\$gw_wif\" == *'name: MODEL_ARMOR_TEMPLATE_ID'* && \"\$gw_wif\" == *'name: GCP_AUTH_PROVIDER_ID'* && \"\$gw_wif\" == *'name: sre-gateway-oauth'* ]]"

echo "true" >"$WORKSHOP_STATE_DIR/governance-enforced"
echo "true" >"$WORKSHOP_STATE_DIR/require-3lo"
echo "sre-triage-guard" >"$WORKSHOP_STATE_DIR/model-armor-template"
echo "projects/11223344/locations/us-central1/authProviders/ops-hub-3lo" >"$WORKSHOP_STATE_DIR/auth-provider-id"
gw_gov="$(IDLE_AFTER=15s DEFAULT_ACTOR_TEMPLATE=sre-agent-v4 bash "$ROOT/scripts/render.sh" "$ROOT/modules/01-actor-boundary/manifests/sre-gateway.yaml.tmpl" 2>&1)"
check "sre-gateway.yaml.tmpl renders Module 05 state files cleanly without unresolved placeholders" \
  "[[ \"\$gw_gov\" == *'value: \"sre-triage-guard\"'* && \"\$gw_gov\" == *'projects/11223344/locations/us-central1/authProviders/ops-hub-3lo'* && \"\$gw_gov\" != *'\${'* ]]"
rm -f "$WORKSHOP_STATE_DIR"/{governance-enforced,require-3lo,model-armor-template,auth-provider-id}

ing_pol="$(bash "$ROOT/scripts/render.sh" "$ROOT/modules/05-zero-trust-governance/manifests/ingress-policy.yaml.tmpl" 2>&1)"
ing_pol_paths="$(awk '/^kind: GCPAuthzPolicy/,0' <<<"$ing_pol" | grep -cE '^[[:space:]]*-[[:space:]]*type:[[:space:]]*(Exact|Prefix|Suffix|Contains)' || true)"
check "ingress-policy.yaml.tmpl renders GCPAgentGateway, HTTPRoute, and GCPAuthzPolicy (<=10 paths) with valid PascalCase StringMatchCriteria" \
  "[[ \"\$ing_pol\" == *'kind: GCPAgentGateway'* && \"\$ing_pol\" == *'name: sre-agent-gateway'* && \"\$ing_pol\" == *'kind: HTTPRoute'* && \"\$ing_pol\" == *'name: sre-ingress-route'* && \"\$ing_pol\" == *'kind: GCPAuthzPolicy'* && \"\$ing_pol\" == *'name: sre-ingress-authz-policy'* && \"\$ing_pol\" == *'action: ALLOW'* && \"\$ing_pol\" == *'enforcementLevel: L7'* && \"\$ing_pol\" == *'policyProfile: REQUEST_AUTHZ'* && \"\$ing_pol\" == *'type: Exact'* && \"\$ing_pol\" == *'type: Prefix'* && \"\$ing_pol\" == *'/.well-known/agent-card.json'* && \"\$ing_pol\" == *'/a2a'* && \"\$ing_pol\" != *'defaultAction'* && \"\$ing_pol\" != *'httpRules'* && \"\$ing_pol\" != *'\${'* && \"\$ing_pol_paths\" -gt 0 && \"\$ing_pol_paths\" -le 10 ]]"

# Module 06: GMP PodMonitoring scraping sre-gateway /metrics.
pod_mon="$(bash "$ROOT/scripts/render.sh" "$ROOT/modules/06-observability-and-evals/manifests/podmonitoring.yaml.tmpl" 2>&1)"
check "podmonitoring.yaml.tmpl renders monitoring.googleapis.com/v1 PodMonitoring/sre-gateway scraping http:/metrics" \
  "[[ \"\$pod_mon\" == *'apiVersion: monitoring.googleapis.com/v1'* && \"\$pod_mon\" == *'kind: PodMonitoring'* && \"\$pod_mon\" == *'name: sre-gateway'* && \"\$pod_mon\" == *'namespace: workshop-301'* && \"\$pod_mon\" == *'port: http'* && \"\$pod_mon\" == *'path: /metrics'* && \"\$pod_mon\" == *'interval: 15s'* && \"\$pod_mon\" != *'\${'* ]]"

# Module 07: Optional GKE Ambient Networking + Secure Web Proxy overlay schema rules.
rfc1918_a="10.0"".0.0/8"
amb_swp="$(bash "$ROOT/scripts/render.sh" "$ROOT/modules/07-wrap-up/manifests/ambient-swp-egress-overlay.yaml.tmpl" 2>&1)"
check "ambient-swp-egress-overlay.yaml.tmpl renders GCPBackend (single serviceUris) and singleton GCPEgressPolicy" \
  "[[ \"\$amb_swp\" == *'apiVersion: networking.gke.io/v1'* && \"\$amb_swp\" == *'kind: GCPBackend'* && \"\$amb_swp\" == *'name: sre-swp-egress-backend'* && \"\$amb_swp\" == *'https://networkservices.googleapis.com/v1/projects/demo-proj/locations/us-central1/gateways/sre-swp-egress-gateway'* && \"\$amb_swp\" == *'kind: GCPEgressPolicy'* && \"\$amb_swp\" == *'name: default'* && \"\$amb_swp\" == *'excludeCIDRRanges:'* && \"\$amb_swp\" == *\"\$rfc1918_a\"* && \"\$amb_swp\" != *'hostname:'* && \"\$amb_swp\" != *'port:'* && \"\$amb_swp\" != *'\${'* ]]"

exit "$fails"
