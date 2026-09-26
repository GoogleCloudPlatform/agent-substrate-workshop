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

# Module 1 end state: Substrate is installed and reachable with kubectl ate.
source "$(dirname "$0")/common.sh"
step "verify module 1 (Substrate installed)"
check "ate-system deployments are Available" kubectl -n ate-system wait --for=condition=Available deploy --all --timeout=10s
check "atelet DaemonSet is rolled out" kubectl -n ate-system rollout status ds --timeout=10s
check ".workshop/worker-image is digest-pinned" bash -c "grep -q '@sha256:' '$STATE_DIR/worker-image'"
check "router parked-request budget is raised" bash -c \
  "kubectl -n ate-system get deploy atenet-router -o jsonpath='{.spec.template.spec.containers[0].args}' | grep -q -- '--parked-request-budget='"
check "nodes carry ate.dev/substrate-version" bash -c \
  "[[ \$(kubectl get nodes -l 'ate.dev/substrate-version=$(state_get substrate-version)' -o name | wc -l) -gt 0 ]]"
check "kubectl ate talks to ateapi" kubectl ate get atespaces
finish 1
