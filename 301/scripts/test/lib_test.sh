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

# Unit tests for scripts/lib.sh, scripts/env.sh, scripts/install-bg.sh, and scripts/patch-router.sh.
# shellcheck disable=SC2329,SC2030,SC2031 # t_* run via check(); per-test exports are subshell-local on purpose
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
export WORKSHOP_ENV_FILE="$TMP/workshop.env" WORKSHOP_STATE_DIR="$TMP/state"
cat >"$WORKSHOP_ENV_FILE" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
BUCKET_NAME=${PROJECT_ID}-ate
EOF
# shellcheck source=../lib.sh
source "$HERE/../lib.sh"

fails=0
check() { local name="$1"; shift; if "$@"; then echo "ok   $name"; else echo "FAIL $name"; fails=$((fails+1)); fi; }

t_load_env_defaults_301() {
  load_env &&
    [[ "$BUCKET_NAME" == "demo-proj-ate" ]] &&
    [[ "$NAMESPACE" == "workshop-301" ]] &&
    [[ "$ATESPACE" == "workshop-301" ]] &&
    [[ "$SIM_NAMESPACE" == "prod-sim" ]] &&
    [[ "$GEMINI_MODEL" == "gemini-3.8-flash" ]] &&
    [[ "$MODEL_LOCATION" == "global" ]] &&
    [[ "$ENABLE_COMPUTE_CLASS_AUTOPROVISION" == "false" ]] &&
    [[ "$CLOUDSDK_CORE_DISABLE_USAGE_REPORTING" == "true" ]]
}

t_load_env_requires_project() {
  local f="$TMP/bad.env"; echo "PROJECT_ID=" >"$f"
  ! (WORKSHOP_ENV_FILE="$f" load_env 2>/dev/null)
}

t_user_has_ate() {
  ! (WORKSHOP_USER_PATH="/usr/bin:/bin" user_has_ate) &&
    (WORKSHOP_USER_PATH="/usr/bin:$REPO_ROOT/bin/" user_has_ate) &&
    [[ "$(WORKSHOP_USER_PATH=/usr/bin path_hint 2>&1)" == *"export PATH=\"$REPO_ROOT/bin:"* ]]
}

t_open_hint() {
  [[ "$(CLOUD_SHELL=true open_hint 8081)" == *"Web Preview"*"8081"* ]] &&
    [[ "$(CLOUD_SHELL='' open_hint 8081)" == "open http://localhost:8081" ]]
}

t_state_roundtrip() {
  state_put agent-image "r/agent@sha256:abc" && [[ "$(state_get agent-image)" == "r/agent@sha256:abc" ]]
}

t_require_state_names_target() {
  local out; out="$( (require_state gateway-image "make gateway") 2>&1 )"
  [[ "$out" == *"make gateway"* ]]
}

t_render_only_listed_vars() {
  printf 'a: ${ATESPACE}\nb: ${HOME_NOT_LISTED}\n' >"$TMP/t.tmpl"
  local out; out="$(render "$TMP/t.tmpl" ATESPACE)"
  [[ "$out" == $'a: workshop-301\nb: ${HOME_NOT_LISTED}' ]]
}

t_render_fails_on_unset() {
  printf 'x: ${NOPE_UNSET}\n' >"$TMP/u.tmpl"
  ! (render "$TMP/u.tmpl" NOPE_UNSET 2>/dev/null)
}

t_wait_for_succeeds() { wait_for 5 "trivial" true; }
t_wait_for_times_out() { ! (wait_for 1 "never" false 2>/dev/null); }

t_port_forward_detects_foreign_listener() {
  local port
  port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
  python3 -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1 & local pid=$!
  sleep 0.4
  local out; out="$( (port_forward testpf default svc/none "$port" 80) 2>&1 )"; local rc=$?
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  [[ $rc -ne 0 && "$out" == *"$port"*"in use"* ]]
}

t_port_forward_reuses_live() {
  local port
  port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
  python3 -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1 & local pid=$!
  sleep 0.4
  echo "$pid" >"$STATE_DIR/pf-reuse.pid"
  port_forward reuse default svc/none "$port" 80; local rc=$?
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -f "$STATE_DIR/pf-reuse.pid"
  [[ $rc -eq 0 ]]
}

t_load_env_mock_llm_override() {
  local f="$TMP/mock.env"; printf 'PROJECT_ID=p\nMOCK_LLM=false\n' >"$f"
  [[ "$(unset MOCK_LLM WORKSHOP_MOCK_LLM_OVERRIDE; WORKSHOP_ENV_FILE="$f" load_env; echo "$MOCK_LLM")" == false ]] &&
    [[ "$(unset WORKSHOP_MOCK_LLM_OVERRIDE; export MOCK_LLM=true WORKSHOP_ENV_FILE="$f"; load_env
         bash -c 'source "$1"; load_env; echo "$MOCK_LLM"' _ "$HERE/../lib.sh")" == true ]]
}

t_load_env_cluster_vars() {
  local n="$TMP/new.env" o="$TMP/old.env" k="$TMP/kctx.env" kb="$TMP/kbin"
  mkdir -p "$kb"
  printf 'PROJECT_ID=p\nCLUSTER_NAME=c1\nCLUSTER_LOCATION=us-east1-b\n' >"$n"
  printf 'PROJECT_ID=p\nCLUSTER=c2\nZONE_OR_REGION=europe-west1\n' >"$o"
  printf 'PROJECT_ID=p\n' >"$k"
  cat >"$kb/kubectl" <<'STUB'
#!/usr/bin/env bash
if [[ "$*" == "config current-context" ]]; then
  echo "${MOCK_KCTX:-}"
fi
exit 0
STUB
  chmod +x "$kb/kubectl"

  [[ "$(unset CLUSTER_NAME CLUSTER_LOCATION; WORKSHOP_ENV_FILE="$n" load_env
       bash -c 'echo "$CLUSTER_NAME/$CLUSTER_LOCATION"')" == "c1/us-east1-b" ]] &&
    [[ "$(unset CLUSTER_NAME CLUSTER_LOCATION; WORKSHOP_ENV_FILE="$o" load_env
         bash -c 'echo "$CLUSTER_NAME/$CLUSTER_LOCATION"')" == "c2/europe-west1" ]] &&
    [[ "$(unset CLUSTER_NAME CLUSTER CLUSTER_LOCATION ZONE_OR_REGION
         MOCK_KCTX="gke_p_us-west1-a_derived-lab" PATH="$kb:$PATH" WORKSHOP_ENV_FILE="$k" load_env
         echo "$CLUSTER_NAME/$CLUSTER_LOCATION")" == "derived-lab/us-west1-a" ]] &&
    [[ "$(unset CLUSTER_NAME CLUSTER CLUSTER_LOCATION ZONE_OR_REGION
         MOCK_KCTX="gke_other-proj_us-west1-a_wrong-cluster" PATH="$kb:$PATH" WORKSHOP_ENV_FILE="$k" load_env
         echo "$CLUSTER_NAME/${CLUSTER_LOCATION:-unset}")" == "ate-cluster/unset" ]]
}

t_install_bg_worker_deploys_prod_sim_and_catchup_accepts_module_0() {
  local d; d="$(mktemp -d "$TMP/bgwk.XXXXXX")"
  mkdir -p "$d/scripts" "$d/state" "$d/bin"
  cp "$HERE/../lib.sh" "$HERE/../install-bg.sh" "$HERE/../catch-up.sh" "$d/scripts/"
  chmod +x "$d/scripts/install-bg.sh" "$d/scripts/catch-up.sh"
  cat >"$d/env" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_LOCATION=us-central1-a
SUBSTRATE_REF=v1.0.0
EOF

  cat >"$d/scripts/install-substrate.sh" <<'STUB'
#!/usr/bin/env bash
echo "install-substrate" >>"$WORKSHOP_STATE_DIR/steps.log"
printf '2026-01-01T00:00:00Z SUBSTRATE_REF=v1.0.0\n' >"$WORKSHOP_STATE_DIR/install-complete"
STUB
  cat >"$d/scripts/prod-sim.sh" <<'STUB'
#!/usr/bin/env bash
echo "prod-sim" >>"$WORKSHOP_STATE_DIR/steps.log"
printf 'r/ledger@sha256:111\n' >"$WORKSHOP_STATE_DIR/ledger-api-image"
printf 'r/opshub@sha256:222\n' >"$WORKSHOP_STATE_DIR/ops-hub-image"
STUB
  cat >"$d/scripts/preflight.sh" <<'STUB'
#!/usr/bin/env bash
echo "preflight" >>"$WORKSHOP_STATE_DIR/steps.log"
STUB
  cat >"$d/scripts/gcp-resources.sh" <<'STUB'
#!/usr/bin/env bash
echo "gcp-resources" >>"$WORKSHOP_STATE_DIR/steps.log"
STUB
  cat >"$d/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
exit 1
STUB
  cat >"$d/bin/make" <<'STUB'
#!/usr/bin/env bash
echo "make-tools" >>"$WORKSHOP_STATE_DIR/steps.log"
STUB
  chmod +x "$d/scripts/"*.sh "$d/bin/kubectl" "$d/bin/make"

  PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$d/scripts/install-bg.sh" --worker >/dev/null 2>&1 || return 1
  [[ "$(paste -sd',' "$d/state/steps.log")" == "install-substrate,prod-sim" ]] || return 1
  [[ "$(cat "$d/state/install-complete")" == *"PROD_SIM=ready"* ]] || return 1

  : >"$d/state/steps.log"
  rm -f "$d/state/ledger-api-image" "$d/state/ops-hub-image" "$d/state/install-complete"
  local cu_out
  cu_out="$(PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$d/scripts/catch-up.sh" 0 2>&1)" || return 1
  [[ "$cu_out" == *"module 00 setup complete"* ]] &&
    [[ "$(paste -sd',' "$d/state/steps.log")" == "preflight,gcp-resources,install-substrate,make-tools,prod-sim" ]]
}

t_stop_port_forward_stops_one() {
  sleep 30 & local a=$!; sleep 30 & local b=$!
  echo "$a" >"$STATE_DIR/pf-one.pid"; echo "$b" >"$STATE_DIR/pf-two.pid"
  stop_port_forward one; stop_port_forward missing
  local ok=true
  kill -0 "$a" 2>/dev/null && ok=false
  [[ -f "$STATE_DIR/pf-one.pid" ]] && ok=false
  kill -0 "$b" 2>/dev/null || ok=false
  stop_port_forwards
  kill -0 "$b" 2>/dev/null && ok=false
  wait "$a" "$b" 2>/dev/null
  $ok
}

t_patch_router_idempotent_spiffe_append() {
  local d; d="$(mktemp -d "$TMP/rtr.XXXXXX")"; mkdir -p "$d/bin" "$d/state/substrate/manifests/ate-install"
  touch "$d/state/substrate/manifests/ate-install/atenet-router-monitoring.yaml"
  cat >"$d/deploy.json" <<'JSON'
{
  "spec": {
    "template": {
      "spec": {
        "terminationGracePeriodSeconds": 60,
        "containers": [
          {
            "name": "router",
            "args": [
              "--listen=:8443",
              "--ingress-allowed-spiffe-ids=spiffe://cluster.local/ns/workshop/sa/gateway,spiffe://cluster.local/ns/ate-system/sa/atenet-router"
            ]
          },
          {
            "name": "envoy",
            "ports": [{"name": "https", "containerPort": 8443}]
          }
        ]
      }
    }
  }
}
JSON
  cat >"$d/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  *"get deploy atenet-router -o json")
    cat "$DEPLOY_JSON" ;;
  *"get deploy atenet-router -o jsonpath"*"containers[0].args"*)
    jq -c '.spec.template.spec.containers[0].args' "$DEPLOY_JSON" ;;
  *"get deploy atenet-router -o jsonpath"*"containers[1].ports"*)
    jq -r '.spec.template.spec.containers[1].ports[].name' "$DEPLOY_JSON" | paste -sd' ' - ;;
  *"get deploy atenet-router -o jsonpath"*"terminationGracePeriodSeconds"*)
    jq -r '.spec.template.spec.terminationGracePeriodSeconds' "$DEPLOY_JSON" ;;
  *"patch deploy atenet-router --type=json -p "*)
    patch="${*: -1}"
    if [[ "$patch" == *'"/spec/template/spec/containers/0/args"'* ]]; then
      val="$(jq -c '.[0].value' <<<"$patch")"
      jq --argjson v "$val" '.spec.template.spec.containers[0].args = $v' "$DEPLOY_JSON" >"$DEPLOY_JSON.tmp" && mv "$DEPLOY_JSON.tmp" "$DEPLOY_JSON"
    elif [[ "$patch" == *"--parked-request-budget=60s"* ]]; then
      jq '.spec.template.spec.containers[0].args += ["--parked-request-budget=60s"]' "$DEPLOY_JSON" >"$DEPLOY_JSON.tmp" && mv "$DEPLOY_JSON.tmp" "$DEPLOY_JSON"
    fi ;;
  *"get svc atenet-router"*) echo "https http" ;;
  *"get networkpolicy atenet-router"*) exit 1 ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/kubectl"
  printf 'PROJECT_ID=demo-proj\nNAMESPACE=workshop-301\n' >"$d/env"
  DEPLOY_JSON="$d/deploy.json" PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$HERE/../patch-router.sh" >/dev/null 2>&1 || return 1
  DEPLOY_JSON="$d/deploy.json" PATH="$d/bin:$PATH" WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$HERE/../patch-router.sh" >/dev/null 2>&1 || return 1

  local spiffe_arg count
  spiffe_arg="$(jq -r '.spec.template.spec.containers[0].args[] | select(startswith("--ingress-allowed-spiffe-ids="))' "$d/deploy.json")"
  count="$(tr ',' '\n' <<<"${spiffe_arg#*=}" | grep -cx 'spiffe://cluster.local/ns/workshop-301/sa/sre-gateway')"
  [[ "$spiffe_arg" == *"spiffe://cluster.local/ns/workshop/sa/gateway"* ]] &&
    [[ "$spiffe_arg" == *"spiffe://cluster.local/ns/ate-system/sa/atenet-router"* ]] &&
    [[ "$count" == "1" ]]
}

t_make_help_lists_every_target() {
  local t missing=0 help; help="$(make -s -C "$REPO_ROOT" help | sed $'s/\e\\[[0-9;]*m//g')"
  for t in $(sed -n 's/^\.PHONY://p' "$REPO_ROOT/Makefile"); do
    [[ "$help" == *" $t "* ]] || { echo "  no help line: $t" >&2; missing=1; }
  done
  return "$missing"
}

t_make_teardown_all_passes_flag() {
  [[ "$(make -s -n -C "$REPO_ROOT" teardown ALL=1)" == "scripts/teardown.sh --all" ]] &&
    [[ "$(make -s -n -C "$REPO_ROOT" teardown)" != *--all* ]]
}

ROOT_DIR="$(cd "$HERE/../.." && pwd)"
interactive() { (cd "$ROOT_DIR" && "$@" </dev/null 2>/dev/null); }

t_lib_refuses_interactive_shell() {
  [[ "$(interactive bash --norc -i -c 'source scripts/lib.sh; echo "alive rc=$?"')" == "alive rc=1" ]]
}

t_env_sh_loads_without_strict_mode() {
  local sh want="demo-proj-ate|workshop-301|$ROOT_DIR|errexit off|still here"
  for sh in bash zsh; do
    command -v "$sh" >/dev/null || continue
    local flags=(-i -c); [[ $sh == bash ]] && flags=(--norc -i -c)
    [[ "$(interactive "$sh" "${flags[@]}" 'source scripts/env.sh; printf "%s|%s|%s|" "$BUCKET_NAME" "$NAMESPACE" "$REPO_ROOT"
      case $- in *e*) printf "errexit on|";; *) printf "errexit off|";; esac; false; echo "still here"')" == "$want" ]] || return 1
  done
}

t_install_status_states() {
  local d="$TMP/inst"; rm -rf "$d"; mkdir -p "$d"
  [[ "$(WORKSHOP_STATE_DIR="$d" bash "$HERE/../install-bg.sh" --status 2>&1)" == *"no background install has been started"* ]] || return 1
  bash -c 'exec -a install-substrate.sh sleep 30' & local live=$!
  echo "$live" >"$d/install.pid"
  [[ "$(WORKSHOP_STATE_DIR="$d" bash "$HERE/../install-bg.sh" --status 2>&1)" == *"install running (pid $live)"* ]] || { kill "$live"; return 1; }
  kill "$live"; wait "$live" 2>/dev/null
  [[ "$(WORKSHOP_STATE_DIR="$d" bash "$HERE/../install-bg.sh" --status 2>&1)" == *"not running and did not finish"* ]] || return 1
  sleep 1; echo "2026-01-01T00:00:00Z SUBSTRATE_REF=v1.0.0" >"$d/install-complete"
  [[ "$(WORKSHOP_STATE_DIR="$d" bash "$HERE/../install-bg.sh" --status 2>&1)" == *"install finished: 2026-01-01T00:00:00Z"* ]]
}

t_install_substrate_preserves_shared_ate_system() {
  local d; d="$(mktemp -d "$TMP/instsub.XXXXXX")"
  mkdir -p "$d/scripts" "$d/mockbin" "$d/state/substrate/.git"
  cp "$HERE/../lib.sh" "$HERE/../install-substrate.sh" "$HERE/../patch-router.sh" "$d/scripts/"
  chmod +x "$d/scripts/install-substrate.sh" "$d/scripts/patch-router.sh"

  cat >"$d/env" <<'EOF'
PROJECT_ID=demo-proj
REGION=us-central1
CLUSTER_LOCATION=us-central1-a
SUBSTRATE_REF=v1.0.0
SUBSTRATE_IMAGE_REPO=ghcr.io/agent-substrate/substrate
SUBSTRATE_IMAGE_TAG=v1.0.0
NAMESPACE=workshop-301
EOF

  cat >"$d/router.json" <<'JSON'
{
  "spec": {
    "template": {
      "spec": {
        "terminationGracePeriodSeconds": 120,
        "containers": [
          {
            "name": "atenet-router",
            "image": "ghcr.io/agent-substrate/substrate/atenet:v1.0.0@sha256:04f7d93dc3fb3deda688137077226c12ce2da41cd4753adff708e3737dc59e20",
            "args": [
              "router",
              "--ingress-allowed-spiffe-ids=spiffe://cluster.local/ns/workshop/sa/gateway,spiffe://cluster.local/ns/aegis-system/sa/aegis-sre,spiffe://cluster.local/ns/ate-system/sa/atenet-router",
              "--parked-request-budget=60s"
            ]
          },
          {
            "name": "envoy",
            "ports": [{"name": "https", "containerPort": 8443}, {"name": "http", "containerPort": 8080}]
          }
        ]
      }
    }
  },
  "status": {
    "conditions": [{"type": "Available", "status": "True"}]
  }
}
JSON

  cat >"$d/mockbin/git" <<'STUB'
#!/usr/bin/env bash
if [[ "$*" == *"describe"* ]]; then echo "v1.0.0"; fi
exit 0
STUB

  cat >"$d/mockbin/go" <<'STUB'
#!/usr/bin/env bash
echo "$*" >>"$GO_CALLS"
case "$*" in
  "build "*kubectl-ate*)
    out=""
    prev=""
    for a in "$@"; do
      [[ "$prev" == "-o" ]] && out="$a"
      prev="$a"
    done
    mkdir -p "$(dirname "$out")"
    printf '#!/usr/bin/env bash\nexit 0\n' >"$out"
    chmod +x "$out" ;;
  *"crane@latest digest"*)
    echo "sha256:f8987236c6256f58c4b2625e543b43f4ae4bdded6ee6821c03d966effb7c0f17" ;;
esac
exit 0
STUB

  cat >"$d/mockbin/kubectl" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  *"get deploy ate-api-server -o json")
    echo '{"spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/agent-substrate/substrate/ateapi:v1.0.0@sha256:2f1ee444b126b6e3620045b61d2a87507fb0f889da0249656c474ab59dc2beaf"}]}}},"status":{"conditions":[{"type":"Available","status":"True"}]}}' ;;
  *"get deploy atenet-router -o json")
    cat "$ROUTER_JSON" ;;
  *"get ds atelet -o json")
    echo '{"spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/agent-substrate/substrate/atelet:v1.0.0@sha256:79abc69f982392adbcbc3332cd8191cf74ac3cd1f4a43c44e9c8a3ce16488b6e"}]}}},"status":{"desiredNumberScheduled":2,"numberReady":2}}' ;;
  *"get workerpools.ate.dev -A -o json")
    echo '{"items":[{"spec":{"workerImage":"ghcr.io/agent-substrate/substrate/ateworker-gvisor:v1.0.0@sha256:f8987236c6256f58c4b2625e543b43f4ae4bdded6ee6821c03d966effb7c0f17"}}]}' ;;
  *"get deploy atenet-router -o jsonpath"*"containers[0].args"*)
    jq -c '.spec.template.spec.containers[0].args' "$ROUTER_JSON" ;;
  *"get deploy atenet-router -o jsonpath"*"containers[1].ports"*)
    jq -r '.spec.template.spec.containers[1].ports[].name' "$ROUTER_JSON" | paste -sd' ' - ;;
  *"get deploy atenet-router -o jsonpath"*"terminationGracePeriodSeconds"*)
    jq -r '.spec.template.spec.terminationGracePeriodSeconds' "$ROUTER_JSON" ;;
  *"patch deploy atenet-router --type=json -p "*)
    patch="${*: -1}"
    if [[ "$patch" == *'"/spec/template/spec/containers/0/args"'* ]]; then
      val="$(jq -c '.[0].value' <<<"$patch")"
      jq --argjson v "$val" '.spec.template.spec.containers[0].args = $v' "$ROUTER_JSON" >"$ROUTER_JSON.tmp" && mv "$ROUTER_JSON.tmp" "$ROUTER_JSON"
    fi ;;
  *"get svc atenet-router"*) echo "https http" ;;
  *"get networkpolicy atenet-router"*) exit 1 ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/mockbin/git" "$d/mockbin/go" "$d/mockbin/kubectl"

  : >"$d/go.calls"
  GO_CALLS="$d/go.calls" ROUTER_JSON="$d/router.json" PATH="$d/mockbin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$d/scripts/install-substrate.sh" >/dev/null 2>&1 || return 1

  # Must NOT invoke ate-setup deploy ate-system when already healthy at v1.0.0.
  ! grep -q "ate-setup deploy ate-system" "$d/go.calls" || return 1
  [[ -x "$d/bin/kubectl-ate" ]] || return 1
  [[ "$(cat "$d/state/worker-image")" == "ghcr.io/agent-substrate/substrate/ateworker-gvisor:v1.0.0@sha256:f8987236c6256f58c4b2625e543b43f4ae4bdded6ee6821c03d966effb7c0f17" ]] || return 1

  local spiffe_arg
  spiffe_arg="$(jq -r '.spec.template.spec.containers[0].args[] | select(startswith("--ingress-allowed-spiffe-ids="))' "$d/router.json")"
  [[ "$spiffe_arg" == *"spiffe://cluster.local/ns/aegis-system/sa/aegis-sre"* ]] || return 1
  [[ "$spiffe_arg" == *"spiffe://cluster.local/ns/workshop/sa/gateway"* ]] || return 1
  [[ "$spiffe_arg" == *"spiffe://cluster.local/ns/workshop-301/sa/sre-gateway"* ]] || return 1

  # With FORCE_REINSTALL_SUBSTRATE=true and explicit ATE_INGRESS_ALLOWED_SPIFFE_IDS, ate-setup runs and STILL merges existing peer SPIFFE IDs.
  : >"$d/go.calls"
  FORCE_REINSTALL_SUBSTRATE=true \
    ATE_INGRESS_ALLOWED_SPIFFE_IDS="spiffe://cluster.local/ns/custom/sa/caller" \
    GO_CALLS="$d/go.calls" ROUTER_JSON="$d/router.json" PATH="$d/mockbin:$PATH" \
    WORKSHOP_ENV_FILE="$d/env" WORKSHOP_STATE_DIR="$d/state" \
    bash "$d/scripts/install-substrate.sh" >/dev/null 2>&1 || return 1

  grep -q "ate-setup deploy ate-system" "$d/go.calls" &&
    grep -q "spiffe://cluster.local/ns/aegis-system/sa/aegis-sre" "$d/go.calls" &&
    grep -q "spiffe://cluster.local/ns/custom/sa/caller" "$d/go.calls"
}

t_ensure_template_retries_transient_upload_and_fails_fast() {
  local d; d="$(mktemp -d "$TMP/etmpl.XXXXXX")"
  mkdir -p "$d/bin"
  echo '{}' >"$d/tmpl.yaml"
  echo "0" >"$d/attempt"

  cat >"$d/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  "ate get actor-template sre-agent-v1 -a workshop-301 -o json")
    n="$(cat "$ATTEMPT_FILE")"
    if [[ "$MODE" == "permanent" ]]; then
      echo '{"status":{"goldenSnapshotStatus":{"errorMessage":"GoldenActorCrashed: wakeup probe failed"}}}'
    elif (( n == 1 )); then
      echo '{"status":{"goldenSnapshotStatus":{"errorMessage":"GoldenActorCrashed: object-store plugin upload of gs://demo/snap failed (Unavailable)"}}}'
    else
      echo '{"status":{"goldenSnapshotStatus":{"goldenTag":{"name":"tag-ok-123"}}}}'
    fi
    exit 0 ;;
  "ate get actor-template sre-agent-v1 -a workshop-301")
    [[ -f "$EXISTS_FILE" ]] && exit 0 || exit 1 ;;
  "ate create actor-template -f "*)
    n=$(( $(cat "$ATTEMPT_FILE") + 1 ))
    echo "$n" >"$ATTEMPT_FILE"
    touch "$EXISTS_FILE"
    exit 0 ;;
  "ate delete actor-template sre-agent-v1 -a workshop-301")
    rm -f "$EXISTS_FILE"
    exit 0 ;;
  "ate get actors -a workshop-301 -o json")
    echo '{"actors":[]}'
    exit 0 ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "$d/bin/kubectl"
  printf '#!/usr/bin/env bash\nexit 0\n' >"$d/bin/sleep"
  chmod +x "$d/bin/sleep"

  # Transient objectstore-plugin upload error should reset and succeed on retry.
  ATTEMPT_FILE="$d/attempt" EXISTS_FILE="$d/exists" MODE="transient" \
    PATH="$d/bin:$PATH" ensure_template workshop-301 sre-agent-v1 "$d/tmpl.yaml" >/dev/null 2>&1 || return 1
  [[ "$(cat "$d/attempt")" == "2" ]] || return 1

  # Permanent errorMessage must fail fast without retrying or hanging 600s.
  rm -f "$d/exists"; echo "0" >"$d/attempt"
  local out rc=0
  out="$( (ATTEMPT_FILE="$d/attempt" EXISTS_FILE="$d/exists" MODE="permanent" \
    PATH="$d/bin:$PATH" ensure_template workshop-301 sre-agent-v1 "$d/tmpl.yaml") 2>&1 )" || rc=$?
  [[ $rc -ne 0 && "$out" == *"wakeup probe failed"* && "$(cat "$d/attempt")" == "1" ]]
}

t_verify00_allows_install_running_and_env_sh_refreshes_user_path() {
  grep -Fq "install_running" "$HERE/../verify/00.sh" &&
    grep -Fq "WORKSHOP_USER_PATH=" "$HERE/../env.sh" || return 1

  # Simulate stale WORKSHOP_USER_PATH before sourcing scripts/env.sh.
  local got_upath
  got_upath="$(cd "$REPO_ROOT" && WORKSHOP_USER_PATH="/usr/bin:/bin" WORK_ENV="$WORKSHOP_ENV_FILE" bash -c '
    export WORKSHOP_ENV_FILE="$WORK_ENV"
    source scripts/env.sh
    printf "%s" "$WORKSHOP_USER_PATH"
  ')"
  [[ "$got_upath" == *"$REPO_ROOT/bin"* ]]
}

t_ephemeral_port_forward_skips_occupied_preferred_port() {
  local d base_port
  d="$(mktemp -d "$TMP/epf.XXXXXX")"
  mkdir -p "$d/bin" "$d/state"
  base_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
  python3 -m http.server "$base_port" --bind 127.0.0.1 >/dev/null 2>&1 &
  local foreign_pid=$!
  sleep 0.3

  cat >"$d/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
mapping="${*: -1}"
lport="${mapping%%:*}"
exec python3 -m http.server "$lport" --bind 127.0.0.1 >/dev/null 2>&1
STUB
  chmod +x "$d/bin/kubectl"

  local ok=false chosen_port="" reused_port=""
  if (
    export PATH="$d/bin:$PATH"
    STATE_DIR="$d/state"
    ephemeral_port_forward verify-gw workshop-301 svc/sre-gateway "$base_port" 8080 || exit 1
    chosen_port="$(cat "$d/state/pf-verify-gw.port")"
    [[ -n "$chosen_port" && "$chosen_port" != "$base_port" ]] || exit 1
    [[ "$PF_URL" == "http://127.0.0.1:$chosen_port" ]] || exit 1
    _port_listening "$chosen_port" || exit 1

    # Idempotent reuse when called a second time while still listening.
    ephemeral_port_forward verify-gw workshop-301 svc/sre-gateway "$base_port" 8080 || exit 1
    reused_port="$(cat "$d/state/pf-verify-gw.port")"
    [[ "$reused_port" == "$chosen_port" ]] || exit 1

    stop_port_forward verify-gw
    [[ ! -f "$d/state/pf-verify-gw.pid" && ! -f "$d/state/pf-verify-gw.port" ]] || exit 1
    ! _port_listening "$chosen_port" || exit 1
    _port_listening "$base_port" || exit 1

    # Verify errexit-disabled caller (`if ephemeral_port_forward ...`) propagates pick_free_port failure cleanly.
    _port_listening() { return 0; }
    if ephemeral_port_forward no-free workshop-301 svc/sre-gateway 65530 8080 2>/dev/null; then
      exit 1
    fi
  ); then
    ok=true
  fi

  kill "$foreign_pid" 2>/dev/null || true
  wait "$foreign_pid" 2>/dev/null || true
  $ok
}

load_env
check "load_env sets 301 defaults and disables gcloud usage reporting" t_load_env_defaults_301
check "load_env requires PROJECT_ID" t_load_env_requires_project
check "user_has_ate checks caller PATH" t_user_has_ate
check "open_hint points Cloud Shell at Web Preview" t_open_hint
check "state roundtrip" t_state_roundtrip
check "require_state names the make target" t_require_state_names_target
check "render substitutes only listed vars" t_render_only_listed_vars
check "render fails on unset var" t_render_fails_on_unset
check "wait_for succeeds" t_wait_for_succeeds
check "wait_for times out" t_wait_for_times_out
check "port_forward detects foreign listener" t_port_forward_detects_foreign_listener
check "port_forward reuses live forward" t_port_forward_reuses_live
check "load_env: command-line MOCK_LLM beats workshop.env" t_load_env_mock_llm_override
check "load_env exports CLUSTER_NAME/CLUSTER_LOCATION and derives from gke_ current-context" t_load_env_cluster_vars
check "install-bg --worker deploys prod-sim and catch-up.sh accepts MODULE=0" t_install_bg_worker_deploys_prod_sim_and_catchup_accepts_module_0
check "stop_port_forward stops only named forward" t_stop_port_forward_stops_one
check "patch-router.sh idempotently appends sre-gateway SPIFFE ID without clobbering existing IDs" t_patch_router_idempotent_spiffe_append
check "make help lists every target" t_make_help_lists_every_target
check "make teardown ALL=1 passes --all" t_make_teardown_all_passes_flag
check "lib.sh refuses interactive shell" t_lib_refuses_interactive_shell
check "env.sh loads workshop.env in bash/zsh without strict mode" t_env_sh_loads_without_strict_mode
check "install-status tracks running, finished, died, and never started" t_install_status_states
check "install-substrate.sh preserves healthy ate-system at SUBSTRATE_REF and merges peer SPIFFE IDs" t_install_substrate_preserves_shared_ate_system
check "ensure_template retries transient objectstore upload errors and fails fast on permanent crashes" t_ensure_template_retries_transient_upload_and_fails_fast
check "verify/00.sh allows install_running for kubectl-ate check and env.sh refreshes WORKSHOP_USER_PATH" t_verify00_allows_install_running_and_env_sh_refreshes_user_path
check "ephemeral_port_forward selects free loopback port when preferred port is occupied" t_ephemeral_port_forward_skips_occupied_preferred_port
exit "$fails"
