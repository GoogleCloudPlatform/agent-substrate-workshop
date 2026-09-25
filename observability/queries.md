# Canned queries

Paste these into the Prometheus UI (`make prometheus-ui` -> http://localhost:9090) or into
Cloud Console -> Monitoring -> Metrics Explorer -> PromQL. Substrate's metric and label names
contain dots, so they are **quoted** (UTF-8 PromQL). Points arrive 1-3 minutes after the action.
Never `kubectl port-forward` by hand for this — `make prometheus-ui`, `make forward-gateway`, and
`make forward-router` set up (and reuse) the port-forwards for you.

## The fleet (substrate)

| Question | PromQL |
|---|---|
| How many workers, in which state (idle/assigned)? | `sum by ("ate.worker.state") ({"ate.workerpool.workers", "ate.workerpool.name"="agents"})` |
| Desired vs ready workers | `{"ate.workerpool.desired_workers", "ate.workerpool.name"="agents"}` and `{"ate.workerpool.ready_workers", "ate.workerpool.name"="agents"}` |
| Lifecycle operations per second | `sum by ("ate.actor.operation.name") (rate({"ate.actor.lifecycle.operation.duration_count"}[2m]))` |
| How slow is a resume? (P95) | `histogram_quantile(0.95, sum by (le, "ate.actor.operation.name") (rate({"ate.actor.lifecycle.operation.duration_bucket"}[5m])))` |
| Restore time by template | `histogram_quantile(0.95, sum by (le, "ate.template.name") (rate({"ate.actor.restore.duration_bucket"}[5m])))` |
| Where does cold start go? (by phase: download, oci_unpack, ateom_restore, ...) | `histogram_quantile(0.95, sum by (le, "ate.snapshot.phase") (rate({"ate.actor.restore.duration_bucket"}[5m])))` |
| Which snapshot kind is being restored? (golden / latest / local) | `sum by ("ate.snapshot.kind") (rate({"ate.actor.restore.duration_count"}[5m]))` |
| Did requests hit a warm actor or trigger a resume? | `sum by ("ate.router.resume") (rate({"atenet.router.route.duration_count", top_level_controller_name="atenet-router"}[2m]))` |
| Router latency, warm vs resumed | `histogram_quantile(0.95, sum by (le, "ate.router.resume") (rate({"atenet.router.route.duration_bucket", top_level_controller_name="atenet-router"}[5m])))` |
| Router requests by outcome (ok, no_capacity, timeout, ...) | `sum by ("ate.router.outcome") (rate({"atenet.router.route.duration_count", top_level_controller_name="atenet-router"}[2m]))` |
| Snapshot image sizes | `histogram_quantile(0.95, sum by (le, "file.name") (rate({"atelet.snapshot.size_bucket"}[10m])))` |
| Actor CPU by template | `sum by ("ate.template.name") (rate({"ate.actor.stats.cpu.time"}[5m]))` |
| Actor memory (working set) by template | `sum by ("ate.template.name") ({"ate.actor.stats.memory.working_set"})` |
| Crashes | `sum(increase({"ate.actor.crashes"}[15m]))` |
| ateapi gRPC errors | `sum by ("rpc.method") (rate({"rpc.server.call.duration_count", top_level_controller_name="ate-api-server", "rpc.response.status_code"!="OK"}[5m]))` |

## Your app (gateway)

| Question | PromQL |
|---|---|
| Chat turns by outcome | `sum by (outcome) (rate(workshop_chat_turns_total[2m]))` |
| Model latency by backend | `histogram_quantile(0.95, sum by (le, backend) (rate(workshop_model_request_duration_seconds_bucket[5m])))` |
| Tokens per minute | `sum by (direction) (rate(workshop_model_tokens_total[5m])) * 60` |
| Awake vs tracked actors | `max(workshop_actors_awake)` / `max(workshop_actors_tracked)` |
| Idle suspends | `sum by (outcome) (increase(workshop_idle_suspends_total[5m]))` |

## Logs (Cloud Console -> Logging -> Logs Explorer)

One actor's history across workers:
```
resource.type="k8s_container"
labels."ate.actor.name"="buddy"
```
Every state change in the atespace:
```
resource.type="k8s_container"
resource.labels.namespace_name="ate-system"
"Actor state changed"
labels."ate.atespace"="workshop"
```
Your agent's own stdout (what `kubectl ate logs actor buddy -a workshop` streams):
```
resource.type="k8s_container"
resource.labels.namespace_name="workshop"
labels."ate.actor.name"="buddy"
```
