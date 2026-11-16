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

# Offline contract tests for Module 03 Procedural Skill bundles (v1 & v2) and scripts/publish-skill.sh.
# shellcheck disable=SC2329 # cleanup and assert_* helpers are invoked indirectly via trap and check()
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
TMP="$(mktemp -d)"
SERVER_PID=""
cleanup() {
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

fails=0
check() {
  local desc="$1"
  shift
  if "$@"; then
    echo "ok   $desc"
  else
    echo "FAIL $desc"
    fails=$((fails + 1))
  fi
}

SKILL_ROOT="$ROOT/modules/03-skill-registry/skills/ledger-pool-triage"
V1_DIR="$SKILL_ROOT/v1"
V2_DIR="$SKILL_ROOT/v2"
PUBLISH_SH="$ROOT/scripts/publish-skill.sh"

check "v1 SKILL.md and executable scripts/diagnose_pool.sh exist" \
  test -f "$V1_DIR/SKILL.md" -a -x "$V1_DIR/scripts/diagnose_pool.sh"
check "v2 SKILL.md and executable scripts/diagnose_pool.sh exist" \
  test -f "$V2_DIR/SKILL.md" -a -x "$V2_DIR/scripts/diagnose_pool.sh"
check "scripts/publish-skill.sh exists and is executable" \
  test -x "$PUBLISH_SH"

# Seed an unpatched ledger-api fixture directory matching ledgerverify.WriteInitialRepoFiles.
REPO_DIR="$TMP/ledger-api"
mkdir -p "$REPO_DIR/config" "$REPO_DIR/pool"
cat >"$REPO_DIR/config/pool.yaml" <<'EOF'
# Connection pool settings for ledger-api.
max_open_conns: 2
idle_timeout_seconds: 0
acquire_timeout_ms: 150
EOF

cat >"$REPO_DIR/pool/store.go" <<'EOF'
// Copyright 2026 Google LLC
package pool

import "context"

func (s *Store) ReconcileTransfer(ctx context.Context, txID string) error {
	rows, err := s.AcquireRows(ctx, txID)
	if err != nil {
		return err
	}
	if err := rows.ValidateChecksum(); err != nil {
		return err
	}
	return rows.Close()
}
EOF

v1_out="$(cd "$REPO_DIR" && sh "$V1_DIR/scripts/diagnose_pool.sh" 2>&1 || true)"
assert_v1_out() { [[ "$v1_out" == *"SKILL_REVISION=v1"* && "$v1_out" == *"MAX_OPEN_CONNS=2"* && "$v1_out" == *"IDLE_TIMEOUT_SECONDS=0"* ]]; }
assert_v1_baseline() { [[ "$v1_out" != *"CURSOR_LEAK_DETECTED=true"* ]]; }
check "v1 diagnose_pool.sh reports SKILL_REVISION=v1 and pool.yaml saturation risk" assert_v1_out
check "v1 diagnose_pool.sh is baseline-only (does not inspect ReconcileTransfer cursor leak)" assert_v1_baseline

v2_out="$(cd "$REPO_DIR" && sh "$V2_DIR/scripts/diagnose_pool.sh" 2>&1 || true)"
assert_v2_out() { [[ "$v2_out" == *"SKILL_REVISION=v2"* && "$v2_out" == *"MAX_OPEN_CONNS=2"* && "$v2_out" == *"CURSOR_LEAK_DETECTED=true"* && "$v2_out" == *"ReconcileTransfer"* ]]; }
check "v2 diagnose_pool.sh reports SKILL_REVISION=v2, pool.yaml, and ReconcileTransfer cursor leak" assert_v2_out

# Verify v2 clears CURSOR_LEAK_DETECTED once defer rows.Close() precedes ValidateChecksum().
cat >"$REPO_DIR/pool/store.go" <<'EOF'
// Copyright 2026 Google LLC
package pool

import "context"

func (s *Store) ReconcileTransfer(ctx context.Context, txID string) error {
	rows, err := s.AcquireRows(ctx, txID)
	if err != nil {
		return err
	}
	defer rows.Close()
	if err := rows.ValidateChecksum(); err != nil {
		return err
	}
	return rows.Close()
}
EOF

v2_patched="$(cd "$REPO_DIR" && sh "$V2_DIR/scripts/diagnose_pool.sh" 2>&1 || true)"
assert_v2_patched() { [[ "$v2_patched" == *"SKILL_REVISION=v2"* && "$v2_patched" == *"CURSOR_LEAK_DETECTED=false"* ]]; }
check "v2 diagnose_pool.sh reports CURSOR_LEAK_DETECTED=false after defer rows.Close() fix" assert_v2_patched

# Verify deterministic archive build produces identical SHA-256 across repeated runs.
ZIP_A="$TMP/v1-a.zip"
ZIP_B="$TMP/v1-b.zip"
ZIP_V2="$TMP/v2.zip"
sha_a="$(bash "$PUBLISH_SH" --build-only "$V1_DIR" "$ZIP_A" 2>/dev/null || true)"
sleep 1
sha_b="$(bash "$PUBLISH_SH" --build-only "$V1_DIR" "$ZIP_B" 2>/dev/null || true)"
sha_v2="$(bash "$PUBLISH_SH" --build-only "$V2_DIR" "$ZIP_V2" 2>/dev/null || true)"

assert_v1_deterministic() { [[ "$sha_a" =~ ^[0-9a-f]{64}$ && "$sha_a" == "$sha_b" ]]; }
assert_v2_distinct() { [[ "$sha_v2" =~ ^[0-9a-f]{64}$ && "$sha_v2" != "$sha_a" ]]; }
check "publish-skill.sh --build-only emits deterministic 64-hex SHA-256 for v1" assert_v1_deterministic
check "v1 and v2 zip archives have distinct SHA-256 digests" assert_v2_distinct

# Start a 127.0.0.1 capture server and verify offline broker sync mode of publish-skill.sh.
CAPTURE_JSON="$TMP/captured_post.json"
PORT_FILE="$TMP/port"
python3 - "$CAPTURE_JSON" "$PORT_FILE" <<'PY' &
import http.server
import json
import sys

capture_path, port_file = sys.argv[1], sys.argv[2]

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length)
        with open(capture_path, "wb") as fh:
            fh.write(raw)
        body = json.loads(raw.decode("utf-8"))
        resp = json.dumps({
            "name": body.get("skill"),
            "default_revision": body.get("revision"),
            "active_sha256": body.get("sha256"),
        }).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)
    def log_message(self, fmt, *args):
        pass

srv = http.server.HTTPServer(("127.0.0.1", 0), Handler)
with open(port_file, "w", encoding="utf-8") as fh:
    fh.write(str(srv.server_address[1]))
srv.handle_request()
PY
SERVER_PID=$!

for _ in $(seq 1 40); do
  [[ -s "$PORT_FILE" ]] && break
  sleep 0.05
done
PORT="$(cat "$PORT_FILE")"

export WORKSHOP_ENV_FILE="$TMP/workshop.env"
export WORKSHOP_STATE_DIR="$TMP/state"
cat >"$WORKSHOP_ENV_FILE" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_LOCATION=us-central1-a
EOF

REV=v2 SKIP_GCLOUD_SKILL_REGISTRY=1 BROKER_SYNC_URL="http://127.0.0.1:${PORT}" \
  bash "$PUBLISH_SH" >/dev/null

assert_sync_json() {
  # shellcheck disable=SC2016 # $sha is a jq variable bound via --arg
  jq -e --arg sha "$sha_v2" '.skill == "ledger-pool-triage" and .revision == "v2" and .sha256 == $sha and .set_default == true and (.archive_base64 | length > 100)' "$CAPTURE_JSON" >/dev/null
}
check "publish-skill.sh syncs archive_base64, revision=v2, and matching sha256 to sre-gateway" assert_sync_json

# Verify publish-skill.sh bounds hung gcloud agent-registry calls (exit 124) without trying alpha track twice.
assert_bounded_gcloud_timeout() {
  local stubdir="$TMP/hangbin"
  mkdir -p "$stubdir"
  cat >"$stubdir/timeout" <<'STUB'
#!/usr/bin/env bash
shift
echo "$*" >>"$GCLOUD_CALL_LOG"
exit 124
STUB
  cat >"$stubdir/gcloud" <<'STUB'
#!/usr/bin/env bash
exit 0
STUB
  chmod +x "$stubdir/timeout" "$stubdir/gcloud"
  : >"$TMP/gcloud.calls"
  GCLOUD_CALL_LOG="$TMP/gcloud.calls" PATH="$stubdir:$PATH" REV=v1 BROKER_SYNC_URL="" \
    WORKSHOP_ENV_FILE="$WORKSHOP_ENV_FILE" WORKSHOP_STATE_DIR="$TMP/state-hang" \
    bash "$PUBLISH_SH" >/dev/null 2>&1 || return 1
  [[ "$(wc -l <"$TMP/gcloud.calls" | tr -d ' ')" == "1" ]] &&
    grep -Fq "gcloud alpha agent-registry skills describe private-ledger-pool-triage --location=global" "$TMP/gcloud.calls"
}
check "publish-skill.sh invokes gcloud alpha with --location=global and short-circuits hung probes on timeout" assert_bounded_gcloud_timeout

assert_sre_gateway_resyncs_staged_skills() {
  local d port srv_pid
  d="$(mktemp -d "$TMP/gw-resync.XXXXXX")"
  mkdir -p "$d/bin" "$d/state"
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

  # Stage v1 followed by v2 (active-rev=v2) offline before rolling sre-gateway.
  REV=v1 SKIP_GCLOUD_SKILL_REGISTRY=1 BROKER_SYNC_URL="" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$PUBLISH_SH" >/dev/null || return 1
  REV=v2 SKIP_GCLOUD_SKILL_REGISTRY=1 BROKER_SYNC_URL="" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$PUBLISH_SH" >/dev/null || return 1

  port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
  python3 - "$port" "$d/synced.jsonl" >"$d/srv.log" 2>&1 <<'PY' &
import http.server, json, sys

port, out_path = int(sys.argv[1]), sys.argv[2]

class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        if self.path == "/readyz":
            body = b'{"service":"sre-gateway","status":"ok"}'
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(body)
        else:
            self.send_response(404); self.end_headers()
    def do_POST(self):
        if self.path == "/api/catalog/skills":
            raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            doc = json.loads(raw.decode("utf-8"))
            with open(out_path, "a", encoding="utf-8") as fh:
                fh.write(json.dumps({
                    "skill": doc.get("skill"),
                    "revision": doc.get("revision"),
                    "set_default": doc.get("set_default"),
                    "cloud_resource": doc.get("cloud_resource"),
                }) + "\n")
            resp = json.dumps({
                "name": doc.get("skill"),
                "default_revision": doc.get("revision") if doc.get("set_default") else "v1",
                "active_sha256": doc.get("sha256"),
            }).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(resp)
        else:
            self.send_response(404); self.end_headers()

http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
PY
  srv_pid=$!
  for _ in $(seq 1 20); do (: <"/dev/tcp/127.0.0.1/$port") >/dev/null 2>&1 && break; sleep 0.05; done

  cat >"$d/bin/kubectl" <<STUB
#!/usr/bin/env bash
case "\$*" in
  *"create namespace"*) printf 'apiVersion: v1\nkind: Namespace\n' ;;
  *"apply -f -"*)       cat >>"$d/applied.yaml"; printf '\n---\n' >>"$d/applied.yaml" ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/kubectl"
  ln -sf "$d/bin/kubectl" "$d/bin/kubectl-ate"

  BROKER_SYNC_URL="http://127.0.0.1:$port" PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/sre-gateway.sh" >/dev/null 2>&1
  local rc=$?
  kill "$srv_pid" 2>/dev/null || true
  wait "$srv_pid" 2>/dev/null || true

  [[ $rc -eq 0 && -s "$d/synced.jsonl" ]] &&
    grep -Fq "name: sre-ingress-route" "$d/applied.yaml" &&
    ! grep -Fq "kind: GCPAuthzPolicy" "$d/applied.yaml" &&
    jq -es '
      length == 2 and
      ([.[].revision] | sort == ["v1", "v2"]) and
      (.[-1].revision == "v2" and .[-1].set_default == true) and
      (.[-1].cloud_resource | contains("/locations/global/skills/private-ledger-pool-triage/revisions/rev-v2"))
    ' "$d/synced.jsonl" >/dev/null
}
check "sre-gateway.sh pre-stages HTTPRoute/sre-ingress-route and re-syncs staged skill revisions after rollout" assert_sre_gateway_resyncs_staged_skills

assert_verify03_accepts_prior_session_ledger_load_skill() {
  grep -Fq "/api/ledger/sessions/inc-402" "$ROOT/scripts/verify/03.sh"
}
check "verify/03.sh accepts load_skill already recorded in inc-402 Session Ledger" assert_verify03_accepts_prior_session_ledger_load_skill

assert_verify03_handles_200kb_session_ledger_without_e2big() {
  local d
  d="$(mktemp -d "$TMP/verify03-e2big.XXXXXX")"
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

  cat >"$d/mock_gw.py" <<'PY'
import http.server, json, sys

port = int(sys.argv[1])
state = {"rev": "v1", "sha": "0" * 64, "revs": [{"revision": "v1"}]}
huge_ledger = json.dumps({
    "session_id": "inc-402",
    "events": [
        {"tool_calls": [{"name": "load_skill"}], "text": "x" * 225000},
    ],
}).encode("utf-8")

class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def _send(self, code, ctype, body):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def do_GET(self):
        if self.path == "/catalog":
            self._send(200, "text/html", b"Procedural skills skills-registry-body skill-revision-drawer")
        elif self.path == "/api/catalog/skills":
            doc = {"skills": [{
                "name": "ledger-pool-triage",
                "default_revision": state["rev"],
                "active_sha256": state["sha"],
                "revisions": state["revs"],
            }]}
            self._send(200, "application/json", json.dumps(doc).encode("utf-8"))
        elif self.path == "/api/ledger/sessions/inc-402":
            self._send(200, "application/json", huge_ledger)
        else:
            self._send(404, "application/json", b"{}")
    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        body = json.loads(raw.decode("utf-8") or "{}")
        if self.path == "/api/catalog/skills":
            rev = body.get("revision", "v1")
            state["rev"] = rev
            state["sha"] = body.get("sha256", "a" * 64)
            if not any(r["revision"] == rev for r in state["revs"]):
                state["revs"].append({"revision": rev})
            self._send(200, "application/json", json.dumps({
                "name": "ledger-pool-triage",
                "default_revision": state["rev"],
                "active_sha256": state["sha"],
            }).encode("utf-8"))
        elif self.path == "/api/chat":
            if state["rev"] == "v1":
                reply = "SKILL_REVISION=v1 MAX_OPEN_CONNS=2"
            else:
                reply = "SKILL_REVISION=v2 CURSOR_LEAK_DETECTED=true ReconcileTransfer"
            self._send(200, "application/json", json.dumps({
                "actor_name": "inc-402",
                "template": "sre-agent-v3",
                "tool_steps": [{"name": "run_workspace_command"}],
                "reply": reply,
            }).encode("utf-8"))
        else:
            self._send(404, "application/json", b"{}")

http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
PY

  cat >"$d/bin/kubectl" <<STUB
#!/usr/bin/env bash
case "\$*" in
  *"ate get actor-template sre-agent-v3"*)
    echo '{"status":{"goldenSnapshotStatus":{"goldenTag":{"name":"golden-v3-tag-1"}}}}'
    ;;
  *"port-forward"*)
    mapping="\${*: -1}"
    lport="\${mapping%%:*}"
    exec python3 "$d/mock_gw.py" "\$lport"
    ;;
  *)
    exit 0
    ;;
esac
STUB
  chmod +x "$d/bin/kubectl"
  ln -sf "$d/bin/kubectl" "$d/bin/kubectl-ate"

  SKIP_GCLOUD_SKILL_REGISTRY=1 PATH="$d/bin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$ROOT/scripts/verify/03.sh" >/dev/null 2>&1
}
check "verify/03.sh succeeds when GET /api/ledger/sessions/inc-402 exceeds 200 KiB" assert_verify03_handles_200kb_session_ledger_without_e2big

exit "$fails"
