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

# Module 2 end state: WorkerPool ready, template agent-v1 has a golden snapshot, actor hello answers.
source "$(dirname "$0")/common.sh"
step "verify module 2 (hello actor)"
check "WorkerPool agents has 2 running worker pods" bash -c \
  "[[ \$(kubectl -n '$NAMESPACE' get pods -l workload=workshop-agent --field-selector=status.phase=Running -o name | wc -l) -ge 2 ]]"
check "template agent-v1 has a golden snapshot" _template_ready "$ATESPACE" agent-v1
check "actor hello answers through the router" bash -c \
  "source '$REPO_ROOT/scripts/lib.sh'; load_env; router_curl hello -f http://localhost:8080/ | grep -q '(mem='"
check "actor hello is RUNNING" bash -c "source '$REPO_ROOT/scripts/lib.sh'; load_env; [[ \$(actor_state '$ATESPACE' hello) == RUNNING ]]"
finish 2
