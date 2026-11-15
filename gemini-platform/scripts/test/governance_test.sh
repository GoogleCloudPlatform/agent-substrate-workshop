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

# Offline contract tests for Module 05 governance setup, Model Armor template JSON,
# and zero-token Cloud Storage / Actor snapshot hygiene auditing.
# shellcheck disable=SC2329,SC2030,SC2031 # t_* functions are invoked dynamically via check()
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT

fails=0
check() { local name="$1"; shift; if "$@"; then echo "ok   $name"; else echo "FAIL $name"; fails=$((fails+1)); fi; }

t_model_armor_template_json_valid() {
  local f="$ROOT/modules/05-zero-trust-governance/manifests/model-armor-template.json"
  [[ -f "$f" ]] &&
    jq -e '
      .filterConfig.piAndJailbreakFilterSettings.filterEnforcement == "ENABLED" and
      .filterConfig.piAndJailbreakFilterSettings.confidenceLevel == "LOW_AND_ABOVE" and
      .filterConfig.maliciousUriFilterSettings.filterEnforcement == "ENABLED" and
      .filterConfig.sdpSettings.basicConfig.filterEnforcement == "ENABLED" and
      .templateMetadata.logSanitizeOperations == true
    ' "$f" >/dev/null
}

_make_audit_fixture() {
  local d="$1"
  mkdir -p "$d/bin" "$d/state" "$d/gcs"
  cat >"$d/env" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_NAME=ate-cluster
CLUSTER_LOCATION=us-central1-a
BUCKET_NAME=${PROJECT_ID}-ate
NAMESPACE=workshop-301
ATESPACE=workshop-301
SIM_NAMESPACE=prod-sim
EOF
  cat >"$d/templates.json" <<'JSON'
{
  "actorTemplates": [
    {
      "metadata": {"name": "sre-agent-v4"},
      "spec": {
        "containers": [
          {
            "name": "agent",
            "env": [
              {"name": "BROKER_URL", "value": "http://sre-gateway.workshop-301.svc.cluster.local:8080"},
              {"name": "ACTOR_TEMPLATE", "value": "sre-agent-v4"}
            ]
          }
        ]
      }
    }
  ]
}
JSON
  cat >"$d/actors.json" <<'JSON'
{
  "actors": [
    {
      "metadata": {"name": "inc-402"},
      "actorTemplate": {"name": "sre-agent-v4"},
      "status": {"state": "ACTOR_STATE_SUSPENDED"}
    }
  ]
}
JSON
  printf '{"snapshot":"clean-memory-page","workspace":"/workspace/ledger-api"}\n' >"$d/gcs/manifest.json"
  printf 'diff --git a/pool/store.go b/pool/store.go\n+defer rows.Close()\n' >"$d/gcs/layer0.blob"

  cat >"$d/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  *"ate get actor-templates"*) cat "$AUDIT_FIXTURE_DIR/templates.json" ;;
  *"ate get actors"*)          cat "$AUDIT_FIXTURE_DIR/actors.json" ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/kubectl"
  ln -sf "$d/bin/kubectl" "$d/bin/kubectl-ate"

  cat >"$d/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  "storage ls -r gs://demo-proj-ate/"*)
    for f in "$AUDIT_FIXTURE_DIR"/gcs/*; do
      [[ -f "$f" ]] && echo "gs://demo-proj-ate/workshop-301/inc-402/$(basename "$f")"
    done
    ;;
  "storage cat gs://demo-proj-ate/workshop-301/inc-402/"*)
    uri="${*: -1}"
    base="${uri##*/}"
    cat "$AUDIT_FIXTURE_DIR/gcs/$base"
    ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/gcloud"
}

t_audit_snapshot_passes_on_clean_artifacts() {
  local d; d="$(mktemp -d "$TMP/audit-clean.XXXXXX")"
  _make_audit_fixture "$d"
  local out rc
  out="$(AUDIT_FIXTURE_DIR="$d" AUDIT_GATEWAY_URL="http://127.0.0.1:1" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/audit-snapshot.sh" 2>&1)"
  rc=$?
  [[ $rc -eq 0 ]] &&
    [[ "$out" == *"ACTOR_SPEC_TOKEN_MATCHES=0"* ]] &&
    [[ "$out" == *"SNAPSHOT_TOKEN_MATCHES=0"* ]] &&
    [[ "$out" == *"SNAPSHOT_OBJECTS_SCANNED=2"* ]]
}

t_audit_snapshot_detects_gcs_and_spec_tokens_without_leaking_values() {
  local d; d="$(mktemp -d "$TMP/audit-dirty.XXXXXX")"
  _make_audit_fixture "$d"
  local fake_tok="opshub_at_secretleak998877665544"
  printf 'memory_page_leak=%s\n' "$fake_tok" >>"$d/gcs/layer0.blob"

  local out rc
  out="$(AUDIT_FIXTURE_DIR="$d" AUDIT_GATEWAY_URL="http://127.0.0.1:1" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/audit-snapshot.sh" 2>&1)"
  rc=$?
  [[ $rc -ne 0 ]] && [[ "$out" == *"SNAPSHOT_TOKEN_MATCHES=1"* ]] && [[ "$out" != *"$fake_tok"* ]] || return 1

  # Restore clean GCS blob and inject a synthetic GCP bearer prefix into Actor spec env instead.
  printf 'clean\n' >"$d/gcs/layer0.blob"
  local fake_gcp="ya29"".DirtySandboxTestTokenValue"
  jq --arg v "$fake_gcp" '.actorTemplates[0].spec.containers[0].env += [{"name":"LEAKED","value":$v}]' \
    "$d/templates.json" >"$d/templates.json.tmp" && mv "$d/templates.json.tmp" "$d/templates.json"

  out="$(AUDIT_FIXTURE_DIR="$d" AUDIT_GATEWAY_URL="http://127.0.0.1:1" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/audit-snapshot.sh" 2>&1)"
  rc=$?
  [[ $rc -ne 0 ]] && [[ "$out" == *"ACTOR_SPEC_TOKEN_MATCHES=1"* ]] && [[ "$out" != *"$fake_gcp"* ]]
}

t_setup_governance_provisions_model_armor_auth_and_ingress_without_agent_rebuild() {
  local d; d="$(mktemp -d "$TMP/gov-setup.XXXXXX")"
  mkdir -p "$d/bin" "$d/state"
  echo "11223344" >"$d/state/project-number"
  echo "r/gateway@sha256:444" >"$d/state/gateway-image"
  cat >"$d/env" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_NAME=ate-cluster
CLUSTER_LOCATION=us-central1-a
BUCKET_NAME=${PROJECT_ID}-ate
NAMESPACE=workshop-301
ATESPACE=workshop-301
SIM_NAMESPACE=prod-sim
EOF

  cat >"$d/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "gcloud $*" >>"$GOV_LOG"
case "$*" in
  "model-armor templates describe "*) exit 1 ;;
  "model-armor templates create "*)   exit 0 ;;
  "agent-identity auth-providers describe "*) exit 1 ;;
  "agent-identity auth-providers create "*)   exit 0 ;;
  "agent-identity auth-providers list"*)      echo "[]" ; exit 0 ;;
  "auth print-access-token"*) echo "stub-gcloud-token" ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/gcloud"

  cat >"$d/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
echo "kubectl $*" >>"$GOV_LOG"
case "$*" in
  *"get crd "*) exit 0 ;;
  *"get gateway sre-ingress-gateway -o jsonpath"*) echo "203.0.113.10" ;;
  *"apply -f -"*)
    cat >>"$APPLIED_YAML"
    echo "---" >>"$APPLIED_YAML"
    ;;
  *"create namespace"*)
    echo "apiVersion: v1"
    echo "kind: Namespace"
    ;;
  *"create secret"*)
    echo "apiVersion: v1"
    echo "kind: Secret"
    ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/kubectl"
  ln -sf "$d/bin/kubectl" "$d/bin/kubectl-ate"

  : >"$d/gov.log"
  : >"$d/applied.yaml"
  # Simulate parent catch-up.sh having already exported empty WORKSHOP_*_OVERRIDE vars while deploy/sre-gateway has live sre-agent-v1.
  WORKSHOP_TEMPLATE_OVERRIDE="" WORKSHOP_GOV_OVERRIDE="" WORKSHOP_3LO_OVERRIDE="" \
    GOV_LOG="$d/gov.log" APPLIED_YAML="$d/applied.yaml" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/setup-governance.sh" >/dev/null 2>&1 || return 1

  [[ "$(cat "$d/state/model-armor-template")" == "sre-triage-guard" ]] &&
    [[ "$(cat "$d/state/governance-enforced")" == "true" ]] &&
    [[ "$(cat "$d/state/require-3lo")" == "true" ]] &&
    [[ -s "$d/state/oauth-client-secret" ]] &&
    grep -q "model-armor templates create sre-triage-guard" "$d/gov.log" &&
    grep -q "kind: GCPAuthzPolicy" "$d/applied.yaml" &&
    grep -q 'value: "sre-agent-v4"' "$d/applied.yaml" &&
    ! grep -q "build.sh agent" "$d/gov.log" || return 1

  # Re-run with GITHUB_OAUTH_CLIENT_ID set to verify Cloud Agent Identity AuthProvider registration.
  cat >>"$d/env" <<'EOF'
GITHUB_OAUTH_CLIENT_ID=Iv1.demo3loclient
GITHUB_OAUTH_CLIENT_SECRET=demo3losecretvalue
EOF
  GOV_LOG="$d/gov.log" APPLIED_YAML="$d/applied.yaml" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/setup-governance.sh" >/dev/null 2>&1 || return 1

  [[ "$(cat "$d/state/auth-provider-id")" == "projects/11223344/locations/us-central1/authProviders/github-3lo" ]] &&
    grep -q "agent-identity auth-providers create github-3lo" "$d/gov.log"
}

t_audit_snapshot_scans_actor_workspace_endpoints_and_rejects_null_or_tokens() {
  local d port srv_pid out rc
  d="$(mktemp -d "$TMP/audit-ws.XXXXXX")"
  _make_audit_fixture "$d"
  printf '{"cloned":true,"repo_dir":"ledger-api","branch":"fix/inc-402-pool-leak","diff":"+max_open_conns: 32"}\n' >"$d/ws.json"

  port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
  python3 - "$port" "$d/ws.json" >"$d/ws-srv.log" 2>&1 <<'PY' &
import http.server, json, pathlib, sys

port = int(sys.argv[1])
ws_file = pathlib.Path(sys.argv[2])

class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        if self.path == "/api/actors":
            body = json.dumps([{"name": "inc-402", "state": "SUSPENDED"}]).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(body)
        elif self.path == "/api/actors/inc-402/workspace":
            body = ws_file.read_bytes()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(body)
        else:
            self.send_response(404)
            self.end_headers()

http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
PY
  srv_pid=$!
  for _ in $(seq 1 20); do (: <"/dev/tcp/127.0.0.1/$port") >/dev/null 2>&1 && break; sleep 0.05; done

  # 1. Clean /api/actors/inc-402/workspace payload passes with WORKSPACE_ACTORS_SCANNED=1 and 0 matches.
  out="$(AUDIT_FIXTURE_DIR="$d" AUDIT_GATEWAY_URL="http://127.0.0.1:$port" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/audit-snapshot.sh" 2>&1)"
  rc=$?
  if [[ $rc -ne 0 || "$out" != *"WORKSPACE_ACTORS_SCANNED=1"* || "$out" != *"WORKSPACE_TOKEN_MATCHES=0"* ]]; then
    kill "$srv_pid" 2>/dev/null || true; wait "$srv_pid" 2>/dev/null || true
    return 1
  fi

  # 2. Leaked GCP, GitHub, and Operations Hub tokens inside /api/actors/inc-402/workspace fail without leaking raw values.
  local fake_gcp="ya29"".WsLeakToken111"
  local fake_gho="gho""_WsLeakToken222"
  local fake_ops="opshub_at""_WsLeakToken333"
  jq -n --arg d "${fake_gcp} ${fake_gho} ${fake_ops}" '{"cloned":true,"diff":$d}' >"$d/ws.json"
  out="$(AUDIT_FIXTURE_DIR="$d" AUDIT_GATEWAY_URL="http://127.0.0.1:$port" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/audit-snapshot.sh" 2>&1)"
  rc=$?
  if [[ $rc -eq 0 || "$out" != *"WORKSPACE_TOKEN_MATCHES=3"* || "$out" == *"$fake_gcp"* || "$out" == *"$fake_gho"* || "$out" == *"$fake_ops"* ]]; then
    kill "$srv_pid" 2>/dev/null || true; wait "$srv_pid" 2>/dev/null || true
    return 1
  fi

  # 3. Null workspace payload fails closed so missing fields never pass silently.
  printf 'null\n' >"$d/ws.json"
  out="$(AUDIT_FIXTURE_DIR="$d" AUDIT_GATEWAY_URL="http://127.0.0.1:$port" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/audit-snapshot.sh" 2>&1)"
  rc=$?
  kill "$srv_pid" 2>/dev/null || true
  wait "$srv_pid" 2>/dev/null || true
  [[ $rc -ne 0 && "$out" == *"null or invalid JSON"* ]]
}

check "model-armor-template.json enables PI/jailbreak, malicious URI, and SDP filters" t_model_armor_template_json_valid
check "audit-snapshot.sh exits 0 with zero matches on clean Actor specs and GCS snapshots" t_audit_snapshot_passes_on_clean_artifacts
check "audit-snapshot.sh fails on leaked tokens in GCS or Actor specs without printing raw tokens" t_audit_snapshot_detects_gcs_and_spec_tokens_without_leaking_values
check "audit-snapshot.sh enumerates /api/actors/{id}/workspace and rejects null or token-bearing payloads" t_audit_snapshot_scans_actor_workspace_endpoints_and_rejects_null_or_tokens
check "setup-governance.sh provisions Model Armor, 3LO AuthProvider, and GCPAuthzPolicy with zero agent rebuilds" t_setup_governance_provisions_model_armor_auth_and_ingress_without_agent_rebuild

t_complete_3lo_pkce_grant_end_to_end() {
  local d port srv_pid rc
  d="$(mktemp -d "$TMP/pkce.XXXXXX")"
  printf 'PROJECT_ID=demo-proj\n' >"$d/env"
  port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
  python3 - "$port" >"$d/srv.log" 2>&1 <<'PY' &
import http.server, json, sys, urllib.parse

port = int(sys.argv[1])

class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_POST(self):
        if self.path == "/api/oauth/start":
            body = json.dumps({
                "consent_required": True,
                "auth_url": f"http://127.0.0.1:{port}/oauth/authorize?response_type=code&client_id=sre-gateway-3lo&state=st123&code_challenge=cc_valid_challenge_string_01234567890123456789&code_challenge_method=S256"
            }).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(body)
        elif self.path == "/oauth/authorize":
            cookie = self.headers.get("Cookie", "")
            raw = self.rfile.read(int(self.headers.get("Content-Length", "0"))).decode()
            form = urllib.parse.parse_qs(raw)
            if "opshub_csrf=csrf999" not in cookie or form.get("csrf_token") != ["csrf999"] or form.get("decision") != ["approve"]:
                self.send_response(403); self.end_headers(); return
            self.send_response(302)
            self.send_header("Location", f"http://127.0.0.1:{port}/api/oauth/callback?code=code777&state=st123")
            self.end_headers()
    def do_GET(self):
        if self.path.startswith("/oauth/authorize"):
            html = (
                '<form method="post" action="/oauth/authorize">'
                '<input type="hidden" name="csrf_token" value="csrf999">'
                '<input type="hidden" name="response_type" value="code">'
                '<input type="hidden" name="client_id" value="sre-gateway-3lo">'
                f'<input type="hidden" name="redirect_uri" value="http://127.0.0.1:{port}/api/oauth/callback">'
                '<input type="hidden" name="state" value="st123">'
                '<input type="hidden" name="scope" value="openid profile remediation:write">'
                '<input type="hidden" name="code_challenge" value="cc_valid_challenge_string_01234567890123456789">'
                '<input type="hidden" name="code_challenge_method" value="S256">'
                '</form>'
            ).encode()
            self.send_response(200)
            self.send_header("Set-Cookie", "opshub_csrf=csrf999; Path=/; HttpOnly")
            self.send_header("Content-Type", "text/html")
            self.end_headers()
            self.wfile.write(html)
        elif self.path == "/api/oauth/callback?code=code777&state=st123":
            if "application/json" not in self.headers.get("Accept", ""):
                self.send_response(400); self.end_headers(); return
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"granted":true,"provider":"ops-hub-3lo"}')

http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
PY
  srv_pid=$!
  for _ in $(seq 1 20); do (: <"/dev/tcp/127.0.0.1/$port") >/dev/null 2>&1 && break; sleep 0.05; done
  WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" bash -c '
    source "$1/scripts/lib.sh"
    load_env
    complete_3lo_pkce_grant "http://127.0.0.1:$2"
  ' _ "$ROOT" "$port"
  rc=$?
  kill "$srv_pid" 2>/dev/null || true
  wait "$srv_pid" 2>/dev/null || true
  [[ $rc -eq 0 ]]
}

check "complete_3lo_pkce_grant executes 4-step OAuth + S256 PKCE + CSRF handshake" t_complete_3lo_pkce_grant_end_to_end

t_gcp_resources_binds_agent_identity_and_wif_principals() {
  local d; d="$(mktemp -d "$TMP/gcp-iam.XXXXXX")"
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

  cat >"$d/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "gcloud $*" >>"$GCP_IAM_LOG"
case "$*" in
  "projects describe demo-proj --format=value(projectNumber)")
    echo "11223344" ;;
  "projects get-ancestors demo-proj --format=json")
    echo '[{"id":"99887766","type":"organization"}]' ;;
  "auth print-access-token"*)
    echo "stub-token" ;;
  *)
    exit 0 ;;
esac
STUB
  cat >"$d/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
exit 1
STUB
  cat >"$d/bin/nohup" <<'STUB'
#!/usr/bin/env bash
exit 0
STUB
  chmod +x "$d/bin/gcloud" "$d/bin/kubectl" "$d/bin/nohup"

  : >"$d/iam.log"
  GCP_IAM_LOG="$d/iam.log" PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/gcp-resources.sh" >/dev/null 2>&1 || return 1

  local wif_member="principal://iam.googleapis.com/projects/11223344/locations/global/workloadIdentityPools/demo-proj.svc.id.goog/subject/ns/workshop-301/sa/sre-gateway"
  local agent_member="principal://agents.global.org-99887766.system.id.goog/resources/container/projects/11223344/locations/us-central1-a/clusters/demo-cluster/ns/workshop-301/sa/sre-gateway"
  grep -Fq "projects add-iam-policy-binding demo-proj --member=${wif_member} --role=roles/aiplatform.user" "$d/iam.log" &&
    grep -Fq "projects add-iam-policy-binding demo-proj --member=${agent_member} --role=roles/aiplatform.user" "$d/iam.log" &&
    grep -Fq "projects add-iam-policy-binding demo-proj --member=${agent_member} --role=roles/modelarmor.user" "$d/iam.log" &&
    grep -Fq "projects add-iam-policy-binding demo-proj --member=${agent_member} --role=roles/serviceusage.serviceUsageConsumer" "$d/iam.log"
}

check "gcp-resources.sh binds Governed Broker IAM roles to both WIF and Pod Agent Identity principals" t_gcp_resources_binds_agent_identity_and_wif_principals

t_ingress_authz_policy_respects_10_path_cloud_limit() {
  local d rendered
  d="$(mktemp -d "$TMP/ing-limit.XXXXXX")"
  mkdir -p "$d/state"
  cat >"$d/env" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_NAME=demo-cluster
CLUSTER_LOCATION=us-central1-a
NAMESPACE=workshop-301
ATESPACE=workshop-301
SIM_NAMESPACE=prod-sim
EOF
  rendered="$(WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/render.sh" "$ROOT/modules/05-zero-trust-governance/manifests/ingress-policy.yaml.tmpl")" || return 1

  python3 - "$rendered" <<'PY' || return 1
import re, sys

docs = [d.strip() for d in sys.argv[1].split("\n---") if d.strip()]
authz_docs = [d for d in docs if re.search(r"^kind:\s*GCPAuthzPolicy\s*$", d, flags=re.M)]
if len(authz_docs) != 1:
    sys.stderr.write(f"expected 1 GCPAuthzPolicy document, got {len(authz_docs)}\n")
    sys.exit(1)

authz = authz_docs[0]
matches = re.findall(r"-\s*type:\s*(Exact|Prefix)\s*\n\s*value:\s*\"?([^\"\n]+)\"?", authz)
if not (1 <= len(matches) <= 10):
    sys.stderr.write(f"GCPAuthzPolicy has {len(matches)} paths (Cloud AuthzPolicy max is 10)\n")
    sys.exit(1)

internal_routes = ["/genai/", "/mcp/", "/ops/git/", "/agentengine/", "/agentregistry/", "/otlp/v1/traces", "/metrics"]
for probe in internal_routes:
    for mtype, val in matches:
        if (mtype == "Exact" and probe == val) or (mtype == "Prefix" and probe.startswith(val)):
            sys.stderr.write(f"internal route {probe} exposed by {mtype} {val}\n")
            sys.exit(1)
PY

  grep -Fq "Programmed" "$ROOT/scripts/verify/05.sh"
}

t_audit_snapshot_targets_atespaces_prefix_on_shared_bucket() {
  local d; d="$(mktemp -d "$TMP/audit-shared-bucket.XXXXXX")"
  _make_audit_fixture "$d"
  local fake_tok="opshub_at_sharedbucketleak99887766"

  cat >"$d/bin/gcloud" <<STUB
#!/usr/bin/env bash
case "\$*" in
  "storage ls -r gs://demo-proj-ate/atespaces/workshop-301/"*)
    echo "gs://demo-proj-ate/atespaces/workshop-301/actors/inc-402/dirty.blob"
    ;;
  "storage ls -r gs://demo-proj-ate/**"*)
    for i in \$(seq -w 1 30); do
      echo "gs://demo-proj-ate/aaa-other-workshop/clean-\${i}.blob"
    done
    echo "gs://demo-proj-ate/atespaces/workshop-301/actors/inc-402/dirty.blob"
    ;;
  "storage cat gs://demo-proj-ate/atespaces/workshop-301/actors/inc-402/dirty.blob")
    printf 'leaked=%s\n' "$fake_tok"
    ;;
  "storage cat gs://demo-proj-ate/aaa-other-workshop/"*)
    printf 'clean\n'
    ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/gcloud"

  local out rc
  out="$(AUDIT_FIXTURE_DIR="$d" AUDIT_GATEWAY_URL="http://127.0.0.1:1" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/audit-snapshot.sh" 2>&1)"
  rc=$?
  [[ $rc -ne 0 && "$out" == *"SNAPSHOT_OBJECTS_SCANNED=1"* && "$out" == *"SNAPSHOT_TOKEN_MATCHES=1"* && "$out" != *"$fake_tok"* ]]
}

check "GCPAuthzPolicy stays within Cloud AuthzPolicy <=10 path limit and verify/05.sh checks Gateway Programmed" t_ingress_authz_policy_respects_10_path_cloud_limit
check "audit-snapshot.sh targets gs://BUCKET/atespaces/ATESPACE/** first on shared buckets" t_audit_snapshot_targets_atespaces_prefix_on_shared_bucket
exit "$fails"
