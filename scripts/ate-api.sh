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
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
load_env
port_forward ate-api ate-system svc/api 8443 443
kubectl -n ate-system create token ate-client --audience api.ate-system.svc --duration 1h >"$STATE_DIR/ate-token"
kubectl get clustertrustbundles -l podcert.ate.dev/canarying=live -o json \
  | jq -r '[.items[] | select(.spec.signerName == "servicedns.podcert.ate.dev/identity") | .spec.trustBundle] | join("\n")' \
  >"$STATE_DIR/ate-ca.pem"
[[ -s "$STATE_DIR/ate-ca.pem" ]] || die "no live ClusterTrustBundle for servicedns.podcert.ate.dev/identity — is podcertcontroller running?"
export ATE_API_ADDR=localhost:8443 ATE_TOKEN_FILE="$STATE_DIR/ate-token" ATE_CA_FILE="$STATE_DIR/ate-ca.pem"
