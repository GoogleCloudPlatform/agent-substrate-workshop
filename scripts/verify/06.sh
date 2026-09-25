#!/usr/bin/env bash
# Module 6 end state: the gateway is scraped, the Prometheus UI answers, and both app and substrate metrics have data.
source "$(dirname "$0")/common.sh"
step "verify module 6 (observability)"
check "PodMonitoring gateway exists" kubectl -n "$NAMESPACE" get podmonitoring gateway
check "prometheus-ui is ready" kubectl -n "$NAMESPACE" rollout status deploy/prometheus-ui --timeout=10s
port_forward prom-ui "$NAMESPACE" svc/prometheus-ui 9090 9090

# has_data <promql>: the query returns at least one series.
has_data() {
  curl -s --get http://localhost:9090/api/v1/query --data-urlencode "query=$1" | jq -e '.data.result | length > 0'
}
# Metrics land in Cloud Monitoring 1–3 minutes after they are produced.
( wait_for 240 "gateway metrics in Cloud Monitoring" has_data 'sum(workshop_chat_turns_total)' ) >/dev/null 2>&1 || true
check "app metric has data: sum(workshop_chat_turns_total)" has_data 'sum(workshop_chat_turns_total)'
check 'substrate metric has data: {"ate.workerpool.workers"}' has_data '{"ate.workerpool.workers"}'
check "workshop dashboard exists" bash -c \
  "gcloud monitoring dashboards list --project '$PROJECT_ID' --filter='displayName=\"Agent Substrate Workshop\"' --format='value(name)' | grep -q ."

# Cloud Logging can lag further than Cloud Monitoring, and a missing log entry doesn't mean the
# telemetry pipeline is broken — so this warns instead of failing the module.
if gcloud logging read \
     "resource.type=\"k8s_container\" AND labels.\"ate.atespace\"=\"$ATESPACE\"" \
     --project="$PROJECT_ID" --freshness=1h --limit=1 --format='value(timestamp)' 2>/dev/null | grep -q .; then
  ok "Cloud Logging has entries for atespace $ATESPACE"
else
  warn "no Cloud Logging entries found yet for atespace $ATESPACE (logs can lag) — check Logs Explorer manually"
fi
finish 6
