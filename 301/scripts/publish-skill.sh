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

# Builds a deterministic ZIP_STORED archive for a Procedural Skill revision, computes its
# SHA-256 digest, publishes/updates the revision in Google Cloud Skill Registry when available,
# and synchronizes the signed archive and defaultRevision to sre-gateway.
#
# Usage:
#   REV=v1 scripts/publish-skill.sh
#   REV=v2 SKILL=ledger-pool-triage scripts/publish-skill.sh
#   scripts/publish-skill.sh --build-only <skill-rev-dir> <out-zip>
set -o errexit -o nounset -o pipefail
export LC_ALL=C

build_deterministic_zip() {
  local src_dir="$1" out_zip="$2"
  [[ -f "$src_dir/SKILL.md" ]] || {
    echo "missing SKILL.md in $src_dir" >&2
    return 1
  }
  mkdir -p "$(dirname "$out_zip")"
  python3 - "$src_dir" "$out_zip" <<'PY'
import hashlib
import os
import sys
import zipfile

src_dir = os.path.abspath(sys.argv[1])
out_zip = os.path.abspath(sys.argv[2])
tmp_zip = out_zip + ".tmp"

entries = []
for root, dirs, files in os.walk(src_dir):
    dirs.sort()
    files.sort()
    for fname in files:
        if fname.startswith("."):
            continue
        full = os.path.join(root, fname)
        if os.path.islink(full):
            raise SystemExit(f"refusing symlink in skill bundle: {full}")
        rel = os.path.relpath(full, src_dir).replace(os.sep, "/")
        entries.append((rel, full))

entries.sort(key=lambda item: item[0])

with zipfile.ZipFile(tmp_zip, "w", compression=zipfile.ZIP_STORED) as zf:
    for rel, full in entries:
        with open(full, "rb") as fh:
            data = fh.read()
        info = zipfile.ZipInfo(filename=rel, date_time=(1980, 1, 1, 0, 0, 0))
        info.compress_type = zipfile.ZIP_STORED
        info.create_system = 3  # Unix
        mode = 0o100755 if (rel.startswith("scripts/") and rel.endswith(".sh")) else 0o100644
        info.external_attr = mode << 16
        zf.writestr(info, data, compress_type=zipfile.ZIP_STORED)

os.replace(tmp_zip, out_zip)
with open(out_zip, "rb") as fh:
    digest = hashlib.sha256(fh.read()).hexdigest()
print(digest)
PY
}

if [[ "${1:-}" == "--build-only" ]]; then
  [[ $# -eq 3 ]] || {
    echo "usage: scripts/publish-skill.sh --build-only <skill-rev-dir> <out-zip>" >&2
    exit 2
  }
  build_deterministic_zip "$2" "$3"
  exit 0
fi

# shellcheck source=scripts/lib.sh
source "$(dirname "$0")/lib.sh"
load_env

SKILL="${SKILL:-ledger-pool-triage}"
REV="${REV:-v1}"
SKILL_REGISTRY_LOCATION="${SKILL_REGISTRY_LOCATION:-global}"
SET_DEFAULT="${SET_DEFAULT:-true}"

[[ "$SKILL" =~ ^[a-z0-9]+(-[a-z0-9]+)*$ ]] || die "invalid SKILL name '$SKILL' (must be lowercase alphanumeric with hyphens)"
[[ "$REV" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || die "invalid REV '$REV' (expected e.g. v1 or v2)"

SKILL_SRC_DIR="${SKILL_DIR:-$REPO_ROOT/modules/03-skill-registry/skills/${SKILL}/${REV}}"
[[ -d "$SKILL_SRC_DIR" ]] || die "skill revision directory not found: $SKILL_SRC_DIR"
[[ -f "$SKILL_SRC_DIR/SKILL.md" ]] || die "missing SKILL.md in $SKILL_SRC_DIR"

ZIP_OUT="$STATE_DIR/skills/${SKILL}-${REV}.zip"
step "Packaging Procedural Skill ${SKILL}@${REV} from ${SKILL_SRC_DIR#"$REPO_ROOT"/}"
SHA256="$(build_deterministic_zip "$SKILL_SRC_DIR" "$ZIP_OUT")"
SIZE_BYTES="$(wc -c <"$ZIP_OUT" | tr -d '[:space:]')"
state_put "skill-${SKILL}-${REV}-sha256" "$SHA256"
if [[ "$SET_DEFAULT" == "true" ]]; then
  state_put "skill-${SKILL}-active-rev" "$REV"
fi
ok "archive built: .workshop/skills/${SKILL}-${REV}.zip (${SIZE_BYTES} bytes, sha256=${SHA256})"

SKILL_DESC="$(awk -F': *' '
  /^---$/ { sep++; next }
  sep == 1 && $1 == "description" { print $2; exit }
' "$SKILL_SRC_DIR/SKILL.md")"
: "${SKILL_DESC:=Procedural diagnostic skill ${SKILL}}"

CLOUD_SKILL="private-${SKILL#private-}"
CLOUD_REV="$(printf '%s' "$REV" | tr '[:upper:]._' '[:lower:]--')"
if (( ${#CLOUD_REV} < 4 )); then
  CLOUD_REV="rev-${CLOUD_REV}"
fi
CLOUD_RES="projects/${PROJECT_ID}/locations/${SKILL_REGISTRY_LOCATION}/skills/${CLOUD_SKILL}/revisions/${CLOUD_REV}"

_run_gcloud_timed() {
  local wall_sec="$1" http_sec="$2"
  shift 2
  if command -v timeout >/dev/null 2>&1; then
    CLOUDSDK_CORE_INTERACTIVE_UX_STYLE=OFF timeout "$wall_sec" "$@" --http-timeout="$http_sec" --quiet
  else
    CLOUDSDK_CORE_INTERACTIVE_UX_STYLE=OFF "$@" --http-timeout="$http_sec" --quiet
  fi
}

publish_gcloud_track() {
  local track_prefix=("$@") rc=0
  _run_gcloud_timed 10 8 "${track_prefix[@]}" agent-registry skills describe "$CLOUD_SKILL" --location="$SKILL_REGISTRY_LOCATION" --project="$PROJECT_ID" >/dev/null 2>&1 || rc=$?
  if (( rc != 0 )); then
    (( rc == 124 )) && return 124
    _run_gcloud_timed 60 30 "${track_prefix[@]}" agent-registry skills create "${SKILL#private-}" \
      --location="$SKILL_REGISTRY_LOCATION" \
      --project="$PROJECT_ID" \
      --display-name="$SKILL" \
      --description="$SKILL_DESC" \
      --type=simple \
      --scanning-policy=disabled \
      --target-state=target-state-draft >/dev/null || return $?
  fi

  rc=0
  _run_gcloud_timed 10 8 "${track_prefix[@]}" agent-registry skills revisions describe "$CLOUD_REV" --skill="$CLOUD_SKILL" --location="$SKILL_REGISTRY_LOCATION" --project="$PROJECT_ID" >/dev/null 2>&1 || rc=$?
  if (( rc != 0 )); then
    (( rc == 124 )) && return 124
    _run_gcloud_timed 60 30 "${track_prefix[@]}" agent-registry skills revisions create "$CLOUD_REV" \
      --skill="$CLOUD_SKILL" \
      --location="$SKILL_REGISTRY_LOCATION" \
      --project="$PROJECT_ID" \
      --payload="$ZIP_OUT" >/dev/null || return $?
  fi

  if [[ "$SET_DEFAULT" == "true" ]]; then
    _run_gcloud_timed 60 30 "${track_prefix[@]}" agent-registry skills update "$CLOUD_SKILL" \
      --location="$SKILL_REGISTRY_LOCATION" \
      --project="$PROJECT_ID" \
      --description="$SKILL_DESC" \
      --target-state=target-state-active \
      --default-revision="$CLOUD_RES" >/dev/null || return $?
  fi
}

if [[ "${SKIP_GCLOUD_SKILL_REGISTRY:-0}" != "1" ]] && command -v gcloud >/dev/null 2>&1; then
  step "Publishing ${SKILL}@${REV} to Google Cloud Skill Registry (${PROJECT_ID}/${SKILL_REGISTRY_LOCATION})"
  gcloud_rc=0
  publish_gcloud_track gcloud alpha 2>"$STATE_DIR/publish-skill-gcloud.log" || gcloud_rc=$?
  if (( gcloud_rc == 0 )); then
    ok "Skill Registry updated via alpha track (${CLOUD_RES}, defaultRevision=${REV})"
  elif (( gcloud_rc != 124 )) && publish_gcloud_track gcloud 2>>"$STATE_DIR/publish-skill-gcloud.log"; then
    ok "Skill Registry updated (${CLOUD_RES}, defaultRevision=${REV})"
  else
    warn "Cloud Skill Registry API unavailable or unreachable from host; continuing with Governed Broker Skill Registry store"
  fi
fi

SYNC_URL="${BROKER_SYNC_URL:-}"
TEMP_PF=false
cleanup_pf() {
  if $TEMP_PF; then
    stop_port_forward publish-skill
  fi
}
trap cleanup_pf EXIT

if [[ -z "$SYNC_URL" ]]; then
  if _is_sre_gateway_url "http://127.0.0.1:8081"; then
    SYNC_URL="http://127.0.0.1:8081"
  elif command -v kubectl >/dev/null 2>&1 && kubectl -n "$NAMESPACE" get svc sre-gateway >/dev/null 2>&1; then
    ephemeral_port_forward publish-skill "$NAMESPACE" svc/sre-gateway 18084 8080
    TEMP_PF=true
    SYNC_URL="$PF_URL"
  fi
fi

if [[ -n "$SYNC_URL" ]]; then
  step "Synchronizing ${SKILL}@${REV} (setDefault=${SET_DEFAULT}) to Governed Broker (${SYNC_URL})"
  PAYLOAD_FILE="$STATE_DIR/skills/${SKILL}-${REV}-sync.json"
  python3 - "$SKILL" "$REV" "$SHA256" "$CLOUD_RES" "$ZIP_OUT" "$PAYLOAD_FILE" "$SET_DEFAULT" <<'PY'
import base64
import json
import sys

skill, rev, sha, cloud_res, zip_path, out_path, set_def_raw = sys.argv[1:8]
with open(zip_path, "rb") as fh:
    b64 = base64.b64encode(fh.read()).decode("ascii")

doc = {
    "skill": skill,
    "revision": rev,
    "sha256": sha,
    "archive_base64": b64,
    "set_default": set_def_raw.lower() == "true",
    "cloud_resource": cloud_res,
}
with open(out_path, "w", encoding="utf-8") as out:
    json.dump(doc, out)
PY

  curl_args=(-fsS -X POST "${SYNC_URL%/}/api/catalog/skills" -H "Content-Type: application/json" --data-binary "@$PAYLOAD_FILE")
  if [[ -n "${BROKER_ADMIN_TOKEN:-}" ]]; then
    curl_args+=(-H "X-Broker-Admin-Token: ${BROKER_ADMIN_TOKEN}")
  fi
  resp="$(curl "${curl_args[@]}")"
  rm -f "$PAYLOAD_FILE"
  if [[ "$SET_DEFAULT" == "true" ]]; then
    got_rev="$(jq -r '.default_revision // empty' <<<"$resp")"
    got_sha="$(jq -r '.active_sha256 // empty' <<<"$resp")"
    [[ "$got_rev" == "$REV" && "$got_sha" == "$SHA256" ]] || \
      die "sre-gateway catalog sync mismatch (got rev=$got_rev sha=$got_sha, want rev=$REV sha=$SHA256)"
    ok "sre-gateway active skill revision: ${SKILL}@${REV} (sha256=${SHA256:0:16}...)"
  else
    ok "sre-gateway staged skill revision: ${SKILL}@${REV} (sha256=${SHA256:0:16}...)"
  fi
else
  warn "sre-gateway not reachable yet; archive staged locally in .workshop/skills/${SKILL}-${REV}.zip"
fi
