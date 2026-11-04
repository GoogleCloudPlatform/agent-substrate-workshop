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

# Source this to talk to ateapi from your laptop the way kubectl-ate does:
#   port-forward svc/api, mint a short-lived token for ate-system/ate-client (audience
#   api.ate-system.svc), and fetch the ClusterTrustBundle that signs ateapi's certificate.
# Scripts source it (scripts/egress.sh). You can too, from the repo root in bash or zsh:
#   source scripts/ate-api.sh
# In an interactive shell the work runs in a bash child, so lib.sh's strict mode and `exit`s never reach
# your shell; only the three ATE_* exports do. This block is POSIX so zsh never parses the bash below.
case $- in *i*)
  if [ ! -f scripts/ate-api.sh ]; then
    echo "run this from the workshop repo root: source scripts/ate-api.sh" >&2
    return 1
  fi
  bash scripts/ate-api.sh || return 1
  export ATE_API_ADDR="localhost:${ATE_API_LOCAL_PORT:-8443}" \
    ATE_TOKEN_FILE="${WORKSHOP_STATE_DIR:-$PWD/.workshop}/ate-token" \
    ATE_CA_FILE="${WORKSHOP_STATE_DIR:-$PWD/.workshop}/ate-ca.pem"
  return 0 ;;
esac
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_env
_ate_api_port="${ATE_API_LOCAL_PORT:-8443}"
port_forward ate-api ate-system svc/api "$_ate_api_port" 443
kubectl -n ate-system create token ate-client --audience api.ate-system.svc --duration 1h >"$STATE_DIR/ate-token"
kubectl get clustertrustbundles -l podcert.ate.dev/canarying=live -o json \
  | jq -r '[.items[] | select(.spec.signerName == "servicedns.podcert.ate.dev/identity") | .spec.trustBundle] | join("\n")' \
  >"$STATE_DIR/ate-ca.pem"
[[ -s "$STATE_DIR/ate-ca.pem" ]] || die "no live ClusterTrustBundle for servicedns.podcert.ate.dev/identity — is podcertcontroller running?"
export ATE_API_ADDR="localhost:${_ate_api_port}" ATE_TOKEN_FILE="$STATE_DIR/ate-token" ATE_CA_FILE="$STATE_DIR/ate-ca.pem"
