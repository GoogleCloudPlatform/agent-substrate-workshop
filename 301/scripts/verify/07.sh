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

# Module 07 end state: optional GKE Ambient Networking + Secure Web Proxy overlay renders cleanly
# with valid networking.gke.io/v1 GCPBackend (single serviceUris) and singleton GCPEgressPolicy
# (excludeCIDRRanges), and teardown / cluster-cleanup scripts pass syntax checks.
# Works both before and after `make teardown` is executed.
# shellcheck source=scripts/verify/common.sh
source "$(dirname "$0")/common.sh"

_ambient_overlay_valid() {
  local out
  out="$("$REPO_ROOT/scripts/render.sh" "$REPO_ROOT/modules/07-wrap-up/manifests/ambient-swp-egress-overlay.yaml.tmpl")" &&
    grep -q 'kind: GCPBackend' <<<"$out" &&
    grep -q 'serviceUris:' <<<"$out" &&
    grep -q "projects/${PROJECT_ID}/locations/${REGION}/gateways/sre-swp-egress-gateway" <<<"$out" &&
    grep -q 'kind: GCPEgressPolicy' <<<"$out" &&
    grep -q 'name: default' <<<"$out" &&
    grep -q 'excludeCIDRRanges:' <<<"$out" &&
    ! grep -qE '^\s*(hostname|port|type):' <<<"$out" &&
    [[ "$out" != *'${'* ]]
}

step "verify module 07 (Wrap-up, optional Ambient egress overlay, and teardown readiness)"
check "Optional Ambient + Secure Web Proxy overlay renders valid GCPBackend & singleton GCPEgressPolicy" \
  _ambient_overlay_valid
check "scripts/teardown.sh and scripts/delete-cluster.sh pass bash -n syntax validation" \
  bash -n "$REPO_ROOT/scripts/teardown.sh" "$REPO_ROOT/scripts/delete-cluster.sh"
check "Module 06 and Module 07 guides exist" \
  test -f "$REPO_ROOT/modules/06-observability-and-evals/README.md" -a -f "$REPO_ROOT/modules/07-wrap-up/README.md"

finish 7
