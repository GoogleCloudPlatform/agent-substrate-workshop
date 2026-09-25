#!/usr/bin/env bash
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
