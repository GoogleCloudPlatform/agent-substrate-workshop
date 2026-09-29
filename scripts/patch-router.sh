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

# Re-applies the workshop's atenet-router settings on top of whatever `ate-setup deploy` rendered: a 60s
# parked-request budget and a 120s grace period, plus the router's GMP PodMonitoring. install-substrate.sh
# runs it; run it again (make patch-router) after anything that re-deploys substrate outside `make install`,
# such as optional lab I's reinstall with experimental flags. Safe to re-run.
source "$(dirname "$0")/lib.sh"
load_env
SRC="$STATE_DIR/substrate"

step "Router: raise parked-request budget to 60s (default 5s is too short for model turns), grace to 120s"
args="$(kubectl -n ate-system get deploy atenet-router -o jsonpath='{.spec.template.spec.containers[0].args}')"
if [[ "$args" != *"--parked-request-budget="* ]]; then
  kubectl -n ate-system patch deploy atenet-router --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--parked-request-budget=60s"}]'
fi
# The router's shutdown runs --drain-delay (13s) + the Envoy drain window (~15s) + the derived
# --drain-timeout (park budget + 10s route budget + 5s margin = 75s at 60s), about 103s plus slack for
# the force stop and telemetry flush. Substrate's default grace of 60s is sized for the 5s budget;
# 120s keeps a router rollout from SIGKILLing parked requests mid-drain.
grace="$(kubectl -n ate-system get deploy atenet-router -o jsonpath='{.spec.template.spec.terminationGracePeriodSeconds}')"
if [[ "${grace:-0}" -lt 120 ]]; then
  kubectl -n ate-system patch deploy atenet-router --type=merge \
    -p '{"spec":{"template":{"spec":{"terminationGracePeriodSeconds":120}}}}'
fi
kubectl -n ate-system rollout status deploy/atenet-router --timeout=5m
ok "router patched"

step "Monitoring for the router (GMP PodMonitoring)"
kubectl apply -f "$SRC/manifests/ate-install/atenet-router-monitoring.yaml"
