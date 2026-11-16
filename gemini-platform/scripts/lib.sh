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

# Shared helpers for workshop scripts. Source this file from non-interactive bash scripts.
case $- in *i*)
  echo "scripts/lib.sh is for workshop scripts. In an interactive shell, run: source scripts/env.sh" >&2
  return 1 ;;
esac
set -euo pipefail

# Disable background gcloud usage reporting forks that race on client certificate cleanup on Linux hosts,
# and disable interactive prompts in non-interactive scripts.
export CLOUDSDK_CORE_DISABLE_USAGE_REPORTING=true
export CLOUDSDK_CORE_DISABLE_PROMPTS=1

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="${WORKSHOP_STATE_DIR:-$REPO_ROOT/.workshop}"
: "${WORKSHOP_USER_PATH:=$PATH}"; export WORKSHOP_USER_PATH
export PATH="$REPO_ROOT/bin:$PATH"

if [[ -t 1 ]]; then C_B=$'\e[1m'; C_G=$'\e[32m'; C_Y=$'\e[33m'; C_R=$'\e[31m'; C_0=$'\e[0m'; else C_B=; C_G=; C_Y=; C_R=; C_0=; fi
step() { echo "${C_B}==> $*${C_0}"; }
ok()   { echo "${C_G}  ✓ $*${C_0}"; }
warn() { echo "${C_Y}  ! $*${C_0}" >&2; }
die()  { echo "${C_R}  ✗ $*${C_0}" >&2; exit 1; }

# load_env reads workshop.env, applies 301 defaults, and validates PROJECT_ID.
load_env() {
  local f="${WORKSHOP_ENV_FILE:-$REPO_ROOT/workshop.env}"
  [[ -f "$f" ]] || die "missing $f; run: cp workshop.env.example workshop.env and set PROJECT_ID and CLUSTER_LOCATION"
  : "${WORKSHOP_MOCK_LLM_OVERRIDE=${MOCK_LLM:-}}"; export WORKSHOP_MOCK_LLM_OVERRIDE
  : "${WORKSHOP_AUTOPROV_OVERRIDE=${ENABLE_COMPUTE_CLASS_AUTOPROVISION:-}}"; export WORKSHOP_AUTOPROV_OVERRIDE
  : "${WORKSHOP_TEMPLATE_OVERRIDE=${DEFAULT_ACTOR_TEMPLATE:-}}"; export WORKSHOP_TEMPLATE_OVERRIDE
  : "${WORKSHOP_GOV_OVERRIDE=${GOVERNANCE_ENFORCED:-}}"; export WORKSHOP_GOV_OVERRIDE
  : "${WORKSHOP_3LO_OVERRIDE=${REQUIRE_3LO_FOR_REMEDIATION:-}}"; export WORKSHOP_3LO_OVERRIDE
  if [[ -z "${WORKSHOP_SUBSTRATE_REPO_SET:-}" && -n "${SUBSTRATE_IMAGE_REPO+x}" ]]; then
    WORKSHOP_SUBSTRATE_REPO_SET=1
    WORKSHOP_SUBSTRATE_REPO_VAL="$SUBSTRATE_IMAGE_REPO"
    export WORKSHOP_SUBSTRATE_REPO_SET WORKSHOP_SUBSTRATE_REPO_VAL
  fi
  if [[ -z "${WORKSHOP_SUBSTRATE_TAG_SET:-}" && -n "${SUBSTRATE_IMAGE_TAG+x}" ]]; then
    WORKSHOP_SUBSTRATE_TAG_SET=1
    WORKSHOP_SUBSTRATE_TAG_VAL="$SUBSTRATE_IMAGE_TAG"
    export WORKSHOP_SUBSTRATE_TAG_SET WORKSHOP_SUBSTRATE_TAG_VAL
  fi
  set -a
  # shellcheck disable=SC1090
  source "$f"
  set +a
  if [[ -n "$WORKSHOP_MOCK_LLM_OVERRIDE" ]]; then MOCK_LLM="$WORKSHOP_MOCK_LLM_OVERRIDE"; fi
  if [[ -n "$WORKSHOP_AUTOPROV_OVERRIDE" ]]; then ENABLE_COMPUTE_CLASS_AUTOPROVISION="$WORKSHOP_AUTOPROV_OVERRIDE"; fi
  if [[ -n "$WORKSHOP_TEMPLATE_OVERRIDE" ]]; then DEFAULT_ACTOR_TEMPLATE="$WORKSHOP_TEMPLATE_OVERRIDE"; fi
  if [[ -n "$WORKSHOP_GOV_OVERRIDE" ]]; then GOVERNANCE_ENFORCED="$WORKSHOP_GOV_OVERRIDE"; fi
  if [[ -n "$WORKSHOP_3LO_OVERRIDE" ]]; then REQUIRE_3LO_FOR_REMEDIATION="$WORKSHOP_3LO_OVERRIDE"; fi
  if [[ "${WORKSHOP_SUBSTRATE_REPO_SET:-}" == 1 ]]; then SUBSTRATE_IMAGE_REPO="$WORKSHOP_SUBSTRATE_REPO_VAL"; fi
  if [[ "${WORKSHOP_SUBSTRATE_TAG_SET:-}" == 1 ]]; then SUBSTRATE_IMAGE_TAG="$WORKSHOP_SUBSTRATE_TAG_VAL"; fi
  [[ -n "${PROJECT_ID:-}" ]] || die "PROJECT_ID is empty in $f"
  : "${REGION:=us-central1}"
  : "${BUCKET_NAME:=${PROJECT_ID}-ate}"
  : "${AR_REPO:=workshop-301}"
  : "${KO_DOCKER_REPO:=${REGION}-docker.pkg.dev/${PROJECT_ID}/${AR_REPO}}"
  : "${KO_DEFAULTPLATFORMS:=linux/amd64}"
  : "${SUBSTRATE_REF:=v1.0.0}"
  : "${NAMESPACE:=workshop-301}"
  : "${ATESPACE:=workshop-301}"
  : "${SIM_NAMESPACE:=prod-sim}"
  : "${MOCK_LLM:=false}"
  : "${GEMINI_MODEL:=gemini-3.8-flash}"
  : "${MODEL_LOCATION:=global}"
  : "${DEFAULT_ACTOR_TEMPLATE:=sre-agent-v1}"
  : "${ENABLE_COMPUTE_CLASS_AUTOPROVISION:=false}"
  if [[ -z "${CLUSTER_NAME:-}" && -z "${CLUSTER:-}" ]] && command -v kubectl >/dev/null 2>&1; then
    local _kctx
    _kctx="$(kubectl config current-context 2>/dev/null || true)"
    if [[ "$_kctx" =~ ^gke_([^_]+)_([^_]+)_(.+)$ ]] && [[ "${BASH_REMATCH[1]}" == "$PROJECT_ID" ]]; then
      CLUSTER_NAME="${BASH_REMATCH[3]}"
      if [[ -z "${CLUSTER_LOCATION:-}" && -z "${ZONE_OR_REGION:-}" ]]; then
        CLUSTER_LOCATION="${BASH_REMATCH[2]}"
      fi
    fi
  fi
  : "${CLUSTER_NAME:=${CLUSTER:-ate-cluster}}"
  : "${CLUSTER_LOCATION:=${ZONE_OR_REGION:-}}"
  : "${OAUTH_CLIENT_ID:=sre-gateway-3lo}"
  export REGION BUCKET_NAME AR_REPO KO_DOCKER_REPO KO_DEFAULTPLATFORMS SUBSTRATE_REF
  export NAMESPACE ATESPACE SIM_NAMESPACE MOCK_LLM GEMINI_MODEL MODEL_LOCATION DEFAULT_ACTOR_TEMPLATE
  export ENABLE_COMPUTE_CLASS_AUTOPROVISION CLUSTER_NAME CLUSTER_LOCATION OAUTH_CLIENT_ID
  mkdir -p "$STATE_DIR"
}

require_cluster_location() {
  [[ -n "${CLUSTER_LOCATION:-}" ]] || die "CLUSTER_LOCATION is empty in workshop.env; set it from the LOCATION column of: gcloud container clusters list --project $PROJECT_ID"
}

# resolve_spiffe_identity populates AGENT_IDENTITY_MODE, SPIFFE_TRUST_DOMAIN,
# SPIFFE_IDENTITY_TYPE, SPIFFE_INJECT_PODCERTIFICATES, SPIFFE_ID_PATTERN, and
# WORKLOAD_PRINCIPAL_PREFIX following org-* -> proj-* -> standard WIF fallback.
resolve_spiffe_identity() {
  local pnum="${PROJECT_NUMBER:-$(state_get project-number || true)}"
  local org="${ORG_ID:-$(state_get org-id || true)}"
  local ai_enabled="${AGENT_IDENTITY_ENABLED:-$(state_get agent-identity-enabled || true)}"
  local mode="${AGENT_IDENTITY_MODE:-auto}"

  if [[ "$mode" == "org" || ( "$mode" == "auto" && -n "$org" ) ]]; then
    [[ -n "$org" ]] || die "AGENT_IDENTITY_MODE=org requires ORG_ID or .workshop/org-id"
    AGENT_IDENTITY_MODE="org"
    SPIFFE_TRUST_DOMAIN="agents.global.org-${org}.system.id.goog"
    SPIFFE_IDENTITY_TYPE="agent-identity"
    SPIFFE_INJECT_PODCERTIFICATES="true"
    SPIFFE_ID_PATTERN="spiffe://${SPIFFE_TRUST_DOMAIN}/*"
    WORKLOAD_PRINCIPAL_PREFIX="principal://${SPIFFE_TRUST_DOMAIN}/resources/container/projects/${pnum:-0}/locations/${CLUSTER_LOCATION:-${REGION}}/clusters/${CLUSTER_NAME}"
  elif [[ "$mode" == "project" || ( "$mode" == "auto" && "$ai_enabled" == "true" && -n "$pnum" ) ]]; then
    [[ -n "$pnum" ]] || die "AGENT_IDENTITY_MODE=project requires PROJECT_NUMBER or .workshop/project-number"
    AGENT_IDENTITY_MODE="project"
    SPIFFE_TRUST_DOMAIN="agents.global.proj-${pnum}.system.id.goog"
    SPIFFE_IDENTITY_TYPE="agent-identity"
    SPIFFE_INJECT_PODCERTIFICATES="true"
    SPIFFE_ID_PATTERN="spiffe://${SPIFFE_TRUST_DOMAIN}/*"
    WORKLOAD_PRINCIPAL_PREFIX="principal://${SPIFFE_TRUST_DOMAIN}/resources/container/projects/${pnum:-0}/locations/${CLUSTER_LOCATION:-${REGION}}/clusters/${CLUSTER_NAME}"
  else
    AGENT_IDENTITY_MODE="wif"
    SPIFFE_TRUST_DOMAIN="${PROJECT_ID}.svc.id.goog"
    SPIFFE_IDENTITY_TYPE="workload-identity"
    SPIFFE_INJECT_PODCERTIFICATES="false"
    SPIFFE_ID_PATTERN=""
    WORKLOAD_PRINCIPAL_PREFIX="principal://iam.googleapis.com/projects/${pnum:-0}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject"
  fi
  export AGENT_IDENTITY_MODE SPIFFE_TRUST_DOMAIN SPIFFE_IDENTITY_TYPE
  export SPIFFE_INJECT_PODCERTIFICATES SPIFFE_ID_PATTERN WORKLOAD_PRINCIPAL_PREFIX
}

install_running() {
  local pid
  pid="$(cat "$STATE_DIR/install.pid" 2>/dev/null)" || return 1
  [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null && ps -o command= -p "$pid" 2>/dev/null | grep -Eq 'install-substrate\.sh|install-bg\.sh'
}

user_has_ate() {
  local p IFS=: want_bin
  want_bin="$(cd "$REPO_ROOT/bin" 2>/dev/null && pwd -P || echo "$REPO_ROOT/bin")"
  for p in $WORKSHOP_USER_PATH; do
    [[ "${p%/}" == "$REPO_ROOT/bin" ]] && return 0
    if [[ -d "$p" ]] && [[ "$(cd "$p" 2>/dev/null && pwd -P)" == "$want_bin" ]]; then
      return 0
    fi
  done
  env PATH="$WORKSHOP_USER_PATH" sh -c 'command -v kubectl-ate' >/dev/null 2>&1
}

path_hint() {
  warn "kubectl ate is missing from your shell PATH. Run:"
  echo "    export PATH=\"$REPO_ROOT/bin:\$PATH\"" >&2
}

open_hint() {
  if [[ "${CLOUD_SHELL:-}" == true ]]; then
    echo "open it with Web Preview (top right of Cloud Shell) > Change port > $1"
  else
    echo "open http://localhost:$1"
  fi
}

state_put() {
  mkdir -p "$STATE_DIR"
  local tmp
  tmp="$(mktemp "$STATE_DIR/.${1}.XXXXXX")"
  printf '%s\n' "$2" >"$tmp"
  mv -f "$tmp" "$STATE_DIR/$1"
}
state_get() { [[ -f "$STATE_DIR/$1" ]] && cat "$STATE_DIR/$1"; }
require_state() { [[ -s "$STATE_DIR/$1" ]] || die ".workshop/$1 is missing; run: $2"; }

wait_for() {
  local timeout="$1" what="$2"; shift 2
  local deadline=$((SECONDS + timeout))
  until "$@" >/dev/null 2>&1; do
    (( SECONDS >= deadline )) && die "timed out after ${timeout}s waiting for: $what"
    sleep 2
  done
}

render() {
  local tmpl="$1"; shift
  local v list=""
  for v in "$@"; do
    if [[ "$v" == "SPIFFE_ID_PATTERN" ]]; then
      [[ -n "${!v+x}" ]] || die "render $tmpl: \$$v is unset"
    else
      [[ -n "${!v:-}" ]] || die "render $tmpl: \$$v is unset"
    fi
    list+="\${$v} "
  done
  envsubst "$list" <"$tmpl"
}

_port_listening() { (: <"/dev/tcp/127.0.0.1/$1") >/dev/null 2>&1; }

_is_sre_gateway_url() {
  curl -fsS --max-time 2 "${1%/}/readyz" 2>/dev/null | jq -e '.service == "sre-gateway"' >/dev/null 2>&1
}

_port_holder() {
  local pid
  pid="$(lsof -t -iTCP:"$1" -sTCP:LISTEN 2>/dev/null | head -1)" || true
  [[ -n "$pid" ]] && echo "pid $pid: $(ps -o command= -p "$pid" 2>/dev/null)"
}

pick_free_port() {
  local base="${1:-18081}" p max_port
  max_port=$(( base + 200 ))
  (( max_port > 65535 )) && max_port=65535
  for (( p = base; p <= max_port; p++ )); do
    if ! _port_listening "$p"; then
      printf '%s\n' "$p"
      return 0
    fi
  done
  die "no free loopback port available in range ${base}-${max_port}"
}

port_forward() {
  local name="$1" ns="$2" res="$3" lport="$4" rport="$5"
  local pidf="$STATE_DIR/pf-$name.pid"
  if [[ -f "$pidf" ]] && kill -0 "$(cat "$pidf")" 2>/dev/null; then
    _port_listening "$lport" && return 0
    kill "$(cat "$pidf")" 2>/dev/null || true
  fi
  rm -f "$pidf"
  if _port_listening "$lport"; then
    local holder; holder="$(_port_holder "$lport")" || true
    die "port $lport is in use by another process${holder:+ ($holder)}; run \`make stop-forwards\` or free the port"
  fi
  kubectl -n "$ns" port-forward --address 127.0.0.1 "$res" "$lport:$rport" >"$STATE_DIR/pf-$name.log" 2>&1 &
  echo $! >"$pidf"
  wait_for 20 "port-forward $name on :$lport" _port_listening "$lport"
}

ephemeral_port_forward() {
  local name="$1" ns="$2" res="$3" lport="$4" rport="$5"
  local pidf="$STATE_DIR/pf-$name.pid" portf="$STATE_DIR/pf-$name.port" existing_port="" chosen=""
  if [[ -f "$pidf" && -f "$portf" ]] && kill -0 "$(cat "$pidf")" 2>/dev/null; then
    existing_port="$(cat "$portf")"
    if [[ -n "$existing_port" ]] && _port_listening "$existing_port"; then
      PF_URL="http://127.0.0.1:$existing_port"
      export PF_URL
      return 0
    fi
  fi
  stop_port_forward "$name"
  chosen="$(pick_free_port "$lport")" || return 1
  port_forward "$name" "$ns" "$res" "$chosen" "$rport" || return 1
  printf '%s\n' "$chosen" >"$portf"
  PF_URL="http://127.0.0.1:$chosen"
  export PF_URL
}

stop_port_forward() {
  local pidf="$STATE_DIR/pf-$1.pid" portf="$STATE_DIR/pf-$1.port" pid i
  if [[ ! -f "$pidf" ]]; then
    rm -f "$portf"
    return 0
  fi
  pid="$(cat "$pidf")"
  kill "$pid" 2>/dev/null || true
  for ((i = 0; i < 20; i++)); do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.05
  done
  rm -f "$pidf" "$portf"
}

stop_port_forwards() {
  local f n
  for f in "$STATE_DIR"/pf-*.pid; do
    [[ -f "$f" ]] || continue
    n="${f##*/pf-}"; stop_port_forward "${n%.pid}"
  done
}

ensure_atespace() {
  kubectl ate get atespace "$1" >/dev/null 2>&1 || kubectl ate create atespace "$1"
  ensure_gateway_access "$1" "${NAMESPACE:-workshop-301}"
}

ensure_gateway_access() {
  local as="${1:-$ATESPACE}" ns="${2:-${NAMESPACE:-workshop-301}}"
  kubectl ate get atespace "$as" >/dev/null 2>&1 || return 0
  kubectl ate get access-policy -a "$as" -o json 2>/dev/null | grep -q "user:system:serviceaccount:${ns}:sre-gateway" && return 0
  kubectl ate create access-policy -a "$as" -f - >/dev/null 2>&1 <<EOF || true
bindings:
  - role: editor
    members:
      - user:system:serviceaccount:${ns}:sre-gateway
EOF
}

ensure_template() {
  local as="$1" name="$2" file="$3" attempt tjson tag err_msg deadline
  for ((attempt = 1; attempt <= 3; attempt++)); do
    kubectl ate get actor-template "$name" -a "$as" >/dev/null 2>&1 || kubectl ate create actor-template -f "$file"
    deadline=$((SECONDS + 600))
    err_msg=""
    while (( SECONDS < deadline )); do
      tjson="$(kubectl ate get actor-template "$name" -a "$as" -o json 2>/dev/null || true)"
      tag="$(jq -r '.status.goldenSnapshotStatus.goldenTag.name // empty' <<<"$tjson" 2>/dev/null || true)"
      [[ -n "$tag" ]] && return 0
      err_msg="$(jq -r '.status.goldenSnapshotStatus.errorMessage // empty' <<<"$tjson" 2>/dev/null || true)"
      [[ -n "$err_msg" ]] && break
      sleep 2
    done
    [[ -z "$err_msg" ]] && die "timed out after 600s waiting for: golden snapshot for template $as/$name"
    if (( attempt < 3 )) && [[ "$err_msg" == *"object-store plugin"* || "$err_msg" == *"Unavailable"* || "$err_msg" == *"PermissionDenied"* ]]; then
      warn "template $as/$name golden snapshot hit transient storage/IAM error (attempt $attempt/3); retrying in 10s..."
      reset_template "$as" "$name"
      sleep 10
      continue
    fi
    die "template $as/$name golden snapshot failed: $err_msg"
  done
}

_template_ready() {
  kubectl ate get actor-template "$2" -a "$1" -o json | jq -e '.status.goldenSnapshotStatus.goldenTag.name' >/dev/null
}

ensure_actor() {
  local as="$1" name="$2" template="$3"
  actor_json "$as" "$name" >/dev/null 2>&1 || kubectl ate create actor "$name" --template="$template" -a "$as"
}

actor_json()   { kubectl ate get actor "$2" -a "$1" -o json; }
actor_state()  { actor_json "$1" "$2" | jq -r '.status.state // "UNKNOWN"' | sed 's/^ACTOR_STATE_//'; }
actor_worker() { actor_json "$1" "$2" | jq -r '.status.workerAssignment.workerPod // ""'; }

_actor_in_state() { [[ "$(actor_state "$1" "$2")" == "$3" ]]; }

_actor_suspended() {
  wait_for 30 "actor $1/$2 to be SUSPENDED" _actor_in_state "$1" "$2" SUSPENDED
}

suspend_actor() {
  local as="$1" name="$2"
  if [[ "$(actor_state "$as" "$name")" == RUNNING ]]; then
    kubectl ate suspend actor "$name" -a "$as" >/dev/null
  fi
  wait_for 120 "actor $as/$name to be SUSPENDED" _actor_in_state "$as" "$name" SUSPENDED
}

_actor_gone() { ! kubectl ate get actor "$2" -a "$1" >/dev/null 2>&1; }

delete_actor_and_wait() {
  local as="$1" name="$2"
  kubectl ate delete actor "$name" -a "$as" >/dev/null 2>&1 || true
  wait_for 120 "actor $as/$name to finish deleting" _actor_gone "$as" "$name"
}

_actors_list() {
  kubectl ate get actors -a "$1" -o json | jq -c 'if type == "array" then . else (.actors // .items // []) end'
}

actor_names() { _actors_list "$1" | jq -r '.[].metadata.name'; }

template_actors() {
  _actors_list "$1" | jq -r --arg t "$2" '.[] | select((.actorTemplate.name // "") == $t) | .metadata.name'
}

_no_template_actors() { [[ -z "$(template_actors "$1" "$2")" ]]; }
_template_gone() { ! kubectl ate get actor-template "$2" -a "$1" >/dev/null 2>&1; }

_delete_template() {
  _template_gone "$1" "$2" || kubectl ate delete actor-template "$2" -a "$1" >/dev/null 2>&1 || true
  _template_gone "$1" "$2"
}

reset_template() {
  local as="$1" t="$2" a
  for a in $(template_actors "$as" "$t"); do
    kubectl ate delete actor "$a" -a "$as" >/dev/null || warn "could not delete actor $as/$a"
  done
  wait_for 180 "actors of template $as/$t to finish deleting" _no_template_actors "$as" "$t"
  wait_for 120 "template $as/$t to be deleted" _delete_template "$as" "$t"
}

count_actors_in_state() {
  _actors_list "$1" | jq --arg s "$2" --arg p "${3:-}" \
    '[.[] | select(((.status.state // "") | sub("^ACTOR_STATE_"; "")) == $s and (.metadata.name | startswith($p)))] | length'
}

# complete_3lo_pkce_grant <gateway_base_url> executes the 4-step OAuth 2.0
# Authorization Code + S256 PKCE + CSRF cookie consent handshake against sre-gateway
# and Operations Hub so verification and catch-up scripts store short-lived tokens
# exclusively in sre-gateway's server-side in-memory vault.
complete_3lo_pkce_grant() {
  local gw="${1%/}" start_json auth_url cookie_jar hdr_file html cb_url cb_json
  start_json="$(curl -fsS -X POST "$gw/api/oauth/start" -H "Content-Type: application/json" -d '{}')" || return 1
  if jq -e '.granted == true and .consent_required == false' <<<"$start_json" >/dev/null 2>&1; then
    return 0
  fi
  auth_url="$(jq -r '.auth_url // empty' <<<"$start_json")"
  [[ -n "$auth_url" ]] || return 1
  mkdir -p "$STATE_DIR"
  cookie_jar="$(mktemp "$STATE_DIR/oauth-cookies.XXXXXX")"
  hdr_file="$(mktemp "$STATE_DIR/oauth-headers.XXXXXX")"
  chmod 600 "$cookie_jar" "$hdr_file"
  html="$(curl -fsS -c "$cookie_jar" "$auth_url")" || { rm -f "$cookie_jar" "$hdr_file"; return 1; }
  _oauth_hidden_field() {
    sed -n "s/.*name=\"$1\" value=\"\\([^\"]*\\)\".*/\\1/p" <<<"$html" | head -n1 | sed 's/&amp;/\&/g'
  }
  local csrf resp_type cid redir state_val scope_val cc ccm
  csrf="$(_oauth_hidden_field csrf_token)"
  resp_type="$(_oauth_hidden_field response_type)"
  cid="$(_oauth_hidden_field client_id)"
  redir="$(_oauth_hidden_field redirect_uri)"
  state_val="$(_oauth_hidden_field state)"
  scope_val="$(_oauth_hidden_field scope)"
  cc="$(_oauth_hidden_field code_challenge)"
  ccm="$(_oauth_hidden_field code_challenge_method)"
  [[ -n "$csrf" && -n "$state_val" && -n "$cc" ]] || { rm -f "$cookie_jar" "$hdr_file"; return 1; }
  curl -fsS -b "$cookie_jar" -D "$hdr_file" -o /dev/null \
    -X POST "$gw/oauth/authorize" \
    --data-urlencode "csrf_token=$csrf" \
    --data-urlencode "response_type=$resp_type" \
    --data-urlencode "client_id=$cid" \
    --data-urlencode "redirect_uri=$redir" \
    --data-urlencode "state=$state_val" \
    --data-urlencode "scope=$scope_val" \
    --data-urlencode "code_challenge=$cc" \
    --data-urlencode "code_challenge_method=$ccm" \
    --data-urlencode "decision=approve" || { rm -f "$cookie_jar" "$hdr_file"; return 1; }
  cb_url="$(awk 'tolower($1)=="location:" {sub(/\r$/,"",$2); print $2}' "$hdr_file" | tail -n1)"
  rm -f "$cookie_jar" "$hdr_file"
  [[ -n "$cb_url" ]] || return 1
  cb_json="$(curl -fsS -H "Accept: application/json" "$cb_url")" || return 1
  jq -e '.granted == true' <<<"$cb_json" >/dev/null
}

# extract_ingress_httproute extracts the HTTPRoute document from rendered
# modules/05-zero-trust-governance/manifests/ingress-policy.yaml.tmpl using
# portable POSIX awk (avoiding GNU-only sed extensions on macOS/BSD).
extract_ingress_httproute() {
  awk '
    /^kind: HTTPRoute$/ { print "apiVersion: gateway.networking.k8s.io/v1"; flag=1 }
    /^---$/ { if (flag) exit }
    flag { print }
  '
}
