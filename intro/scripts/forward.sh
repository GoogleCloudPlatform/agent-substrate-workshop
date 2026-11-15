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

# forward.sh <name> <namespace> <resource> <local-port> <remote-port>: (re)start a background port-forward
# the same way the make forward-* targets do. `make stop-forwards` stops it.
# Example: scripts/forward.sh conf-gateway ate-demo-conf svc/conf-gateway 8090 80
source "$(dirname "$0")/lib.sh"
load_env
(( $# == 5 )) || die "usage: scripts/forward.sh <name> <namespace> <resource> <local-port> <remote-port>"
# Always restart: after the Service's pods change, an old kubectl port-forward keeps its local port open
# but every connection through it fails, so "still listening" doesn't mean "still working".
stop_port_forward "$1"
port_forward "$@"
ok "$1: http://localhost:$4"
