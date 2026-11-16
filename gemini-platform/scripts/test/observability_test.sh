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

# Offline contract tests for Module 06 observability.sh, Module 07 Ambient SWP schema,
# and idempotent teardown.sh (--all) cleanup.
# shellcheck disable=SC2329,SC2030,SC2031
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT

fails=0
check() { local name="$1"; shift; if "$@"; then echo "ok   $name"; else echo "FAIL $name"; fails=$((fails+1)); fi; }

_make_obs_fixture() {
  local d="$1" has_crd="$2"
  mkdir -p "$d/bin" "$d/state"
  cat >"$d/env" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_NAME=demo-cluster
CLUSTER_LOCATION=us-central1-a
BUCKET_NAME=${PROJECT_ID}-ate
NAMESPACE=workshop-301
ATESPACE=workshop-301
SIM_NAMESPACE=prod-sim
EOF

  cat >"$d/bin/kubectl" <<STUB
#!/usr/bin/env bash
echo "\$*" >>"$d/kubectl.log"
case "\$*" in
  *"wait --for=condition=Available deploy/sre-gateway"*) exit 0 ;;
  *"get crd podmonitorings.monitoring.googleapis.com"*)
    [[ "$has_crd" == "true" ]] && exit 0 || exit 1
    ;;
  *"apply -f"*) exit 0 ;;
  *"ate get atespace"*) exit 0 ;;
  *"ate get actors"*)
    if [[ ! -f "$d/actors-listed-once" ]]; then
      touch "$d/actors-listed-once"
      echo '{"actors":[{"metadata":{"name":"inc-402"}}]}'
    else
      echo '{"actors":[]}'
    fi
    ;;
  *"ate get actor-templates"*) echo '{"actorTemplates":[{"metadata":{"name":"sre-agent-v4"}}]}' ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/kubectl"
  ln -sf "$d/bin/kubectl" "$d/bin/kubectl-ate"

  cat >"$d/bin/gcloud" <<STUB
#!/usr/bin/env bash
echo "\$*" >>"$d/gcloud.log"
case "\$*" in
  "auth print-access-token"*) echo "stub-access-token" ;;
  "projects describe"*) echo "11223344" ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/gcloud"

  cat >"$d/bin/curl" <<STUB
#!/usr/bin/env bash
echo "\$*" >>"$d/curl.log"
exit 0
STUB
  chmod +x "$d/bin/curl"
}

t_observability_applies_podmonitoring_when_crd_exists() {
  local d; d="$(mktemp -d "$TMP/obs-crd.XXXXXX")"
  _make_obs_fixture "$d" "true"
  local out rc
  out="$(PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/observability.sh" 2>&1)"
  rc=$?
  [[ $rc -eq 0 ]] &&
    [[ -s "$d/state/podmonitoring.yaml" ]] &&
    [[ "$(cat "$d/state/observability-enabled")" == "true" ]] &&
    grep -q "apply -f $d/state/podmonitoring.yaml" "$d/kubectl.log" &&
    [[ "$out" == *"console.cloud.google.com/traces/list?project=demo-proj"* ]]
}

t_observability_warns_gracefully_without_gmp_crd() {
  local d; d="$(mktemp -d "$TMP/obs-nocrd.XXXXXX")"
  _make_obs_fixture "$d" "false"
  local out rc
  out="$(PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/observability.sh" 2>&1)"
  rc=$?
  [[ $rc -eq 0 ]] &&
    [[ -s "$d/state/podmonitoring.yaml" ]] &&
    ! grep -q "apply -f" "$d/kubectl.log"
}

t_ambient_swp_strict_gcpbackend_schema() {
  local d; d="$(mktemp -d "$TMP/amb.XXXXXX")"
  _make_obs_fixture "$d" "true"
  local rendered
  rendered="$(WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/render.sh" "$ROOT/modules/07-wrap-up/manifests/ambient-swp-egress-overlay.yaml.tmpl")" || return 1

  # GCPBackend referenced by GCPEgressPolicy must contain ONLY spec.serviceUris with 1 URI.
  local uri_count rfc1918_a="10.0"".0.0/8"
  uri_count="$(grep -cE '^\s*-\s*https://networkservices\.googleapis\.com/v1/projects/demo-proj/locations/us-central1/gateways/sre-swp-egress-gateway$' <<<"$rendered" || true)"
  [[ "$uri_count" -eq 1 ]] &&
    ! grep -qE '^\s*(hostname|port|type):' <<<"$rendered" &&
    grep -qE '^\s*name:\s*default$' <<<"$rendered" &&
    grep -q "$rfc1918_a" <<<"$rendered" &&
    grep -q '172.16.0.0/12' <<<"$rendered" &&
    grep -q '192.168.0.0/16' <<<"$rendered"
}

t_teardown_all_cleans_podmonitoring_computeclass_armor_and_skills() {
  local d; d="$(mktemp -d "$TMP/teardown.XXXXXX")"
  _make_obs_fixture "$d" "true"
  # Simulate empty actors after first delete call so wait_for exits immediately.
  cat >"$d/bin/kubectl" <<STUB
#!/usr/bin/env bash
echo "\$*" >>"$d/kubectl.log"
case "\$*" in
  *"ate get atespace"*) exit 0 ;;
  *"ate get actors"*)
    if [[ ! -f "$d/actors-listed-once" ]]; then
      touch "$d/actors-listed-once"
      echo '{"actors":[{"metadata":{"name":"inc-402"}}]}'
    else
      echo '{"actors":[]}'
    fi
    ;;
  *"ate get actor-templates"*) echo '{"actorTemplates":[{"metadata":{"name":"sre-agent-v4"}}]}' ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/kubectl"
  ln -sf "$d/bin/kubectl" "$d/bin/kubectl-ate"

  echo "998877" >"$d/state/reasoning-engine-id"
  echo "sre-triage-guard" >"$d/state/model-armor-template"
  echo "projects/11223344/locations/us-central1/authProviders/ops-hub-3lo" >"$d/state/auth-provider-id"
  echo "true" >"$d/state/observability-enabled"
  echo "{}" >"$d/state/eval-report.json"

  local out rc
  out="$(YES=1 PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/teardown.sh" --all 2>&1)"
  rc=$?
  [[ $rc -eq 0 ]] &&
    grep -q "delete podmonitoring sre-gateway" "$d/kubectl.log" &&
    grep -q "delete computeclass substrate-gvisor" "$d/kubectl.log" &&
    grep -q "delete ns workshop-301 prod-sim" "$d/kubectl.log" &&
    grep -q "storage rm -r gs://demo-proj-ate/atespaces/workshop-301/\*\* --quiet" "$d/gcloud.log" &&
    grep -q "model-armor templates delete sre-triage-guard" "$d/gcloud.log" &&
    grep -q "agent-identity auth-providers delete ops-hub-3lo" "$d/gcloud.log" &&
    grep -q "alpha agent-registry skills delete private-ledger-pool-triage --location=global" "$d/gcloud.log" &&
    grep -q "alpha agent-registry skills delete ledger-pool-triage --location=global" "$d/gcloud.log" &&
    grep -q "agentregistry.googleapis.com/v1/projects/demo-proj/locations/global/skills/private-ledger-pool-triage?force=true" "$d/curl.log" &&
    [[ ! -f "$d/state/eval-report.json" ]] &&
    [[ ! -f "$d/state/observability-enabled" ]] &&
    [[ "$out" == *"teardown complete"* ]]
}

t_delete_cluster_releases_sre_gateway_before_pvcs() {
  local d; d="$(mktemp -d "$TMP/del-cluster.XXXXXX")"
  _make_obs_fixture "$d" "true"
  echo "CLUSTER_LOCATION=us-central1-a" >>"$d/env"
  echo "CLUSTER_NAME=demo-cluster" >>"$d/env"
  local out rc
  out="$(YES=1 PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/delete-cluster.sh" 2>&1)"
  rc=$?
  local gw_line pvc_line
  gw_line="$(grep -n "delete deploy sre-gateway" "$d/kubectl.log" | head -n1 | cut -d: -f1)"
  pvc_line="$(grep -n "delete pvc --all" "$d/kubectl.log" | head -n1 | cut -d: -f1)"
  [[ $rc -eq 0 && -n "$gw_line" && -n "$pvc_line" && "$gw_line" -lt "$pvc_line" && "$out" == *"cluster deleted"* ]]
}

t_teardown_without_all_cleans_workshop_sessions() {
  local d; d="$(mktemp -d "$TMP/teardown-sessions.XXXXXX")"
  _make_obs_fixture "$d" "true"
  echo "998877" >"$d/state/reasoning-engine-id"
  local rc
  YES=1 PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/teardown.sh" >/dev/null 2>&1
  rc=$?
  [[ $rc -eq 0 ]] &&
    grep -q "reasoningEngines/998877/sessions/inc-402" "$d/curl.log" &&
    grep -q "reasoningEngines/998877/sessions/inc-403" "$d/curl.log" &&
    grep -q "reasoningEngines/998877/sessions/inc-409" "$d/curl.log"
}

t_teardown_cba_deletes_sessions_via_sre_gateway_with_substrate_app_name() {
  local d; d="$(mktemp -d "$TMP/teardown-cba.XXXXXX")"
  _make_obs_fixture "$d" "true"
  rm -f "$d/bin/curl"

  cat >"$d/bin/gcloud" <<STUB
#!/usr/bin/env bash
case "\$*" in
  "config get-value context_aware/use_client_certificate"*) echo "true" ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/gcloud"

  cat >"$d/mock_gw.py" <<'PY'
import http.server, sys

port, out_path = int(sys.argv[1]), sys.argv[2]

class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        self.send_response(404); self.end_headers()
    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", "0"))).decode("utf-8")
        with open(out_path, "a", encoding="utf-8") as fh:
            fh.write(f"{self.path} {raw}\n")
        body = b'{"deleted":"ok"}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
PY

  cat >"$d/bin/kubectl" <<STUB
#!/usr/bin/env bash
case "\$*" in
  *"get endpoints sre-gateway"*) echo "10.24.0.9" ;;
  *"port-forward"*)
    mapping="\${*: -1}"
    lport="\${mapping%%:*}"
    exec python3 "$d/mock_gw.py" "\$lport" "$d/gw-deletes.log"
    ;;
  *"ate get actors"*)
    if [[ ! -f "$d/actors-listed-once" ]]; then
      touch "$d/actors-listed-once"
      echo '{"actors":[{"metadata":{"name":"inc-402"}}]}'
    else
      echo '{"actors":[]}'
    fi
    ;;
  *"ate get actor-templates"*) echo '{"actorTemplates":[]}' ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/kubectl"
  ln -sf "$d/bin/kubectl" "$d/bin/kubectl-ate"

  YES=1 PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/teardown.sh" >/dev/null 2>&1 || return 1

  [[ -s "$d/gw-deletes.log" ]] &&
    grep -Fq '/agentengine/sessions:delete {"app_name":"substrate-sre-triage","user_id":"sre-operator","session_id":"inc-402"}' "$d/gw-deletes.log" &&
    grep -Fq '/agentengine/sessions:delete {"app_name":"substrate-sre-triage","user_id":"sre-operator","session_id":"inc-403"}' "$d/gw-deletes.log" &&
    grep -Fq '/agentengine/sessions:delete {"app_name":"substrate-sre-triage","user_id":"sre-operator","session_id":"inc-409"}' "$d/gw-deletes.log"
}

check "observability.sh renders and applies PodMonitoring/sre-gateway when CRD exists" t_observability_applies_podmonitoring_when_crd_exists
check "observability.sh degrades gracefully without GMP CRD" t_observability_warns_gracefully_without_gmp_crd
check "ambient-swp-egress-overlay.yaml.tmpl enforces single-URI GCPBackend and singleton GCPEgressPolicy" t_ambient_swp_strict_gcpbackend_schema
check "teardown.sh --all cleans PodMonitoring, ComputeClass, namespaces, Model Armor, AuthProvider, and Skill Registry" t_teardown_all_cleans_podmonitoring_computeclass_armor_and_skills
check "teardown.sh without --all deletes workshop sessions on ReasoningEngine" t_teardown_without_all_cleans_workshop_sessions
check "teardown.sh on CBA hosts deletes substrate-sre-triage sessions via sre-gateway" t_teardown_cba_deletes_sessions_via_sre_gateway_with_substrate_app_name
check "delete-cluster.sh deletes deploy/sre-gateway before releasing PVCs" t_delete_cluster_releases_sre_gateway_before_pvcs
exit "$fails"
