# Canned queries

Paste these into the Prometheus UI (`make prometheus-ui` -> http://localhost:9090) or into
Cloud Console -> Monitoring -> Metrics Explorer -> PromQL. Substrate's metric and label names
contain dots, so they are **quoted** (UTF-8 PromQL). Points arrive 1-3 minutes after the action.
Never `kubectl port-forward` by hand for this — `make prometheus-ui`, `make forward-gateway`, and
`make forward-router` set up (and reuse) the port-forwards for you.

## The fleet (substrate)

| Question | PromQL |
|---|---|
| How many workers, in which state (idle/assigned)? Every ateapi replica reports the whole pool, so take `max`, not `sum` | `max by ("ate.worker.state") ({"ate.workerpool.workers", "ate.workerpool.name"="agents"})` |
| Desired vs ready workers | `{"ate.workerpool.desired_workers", "ate.workerpool.name"="agents"}` and `{"ate.workerpool.ready_workers", "ate.workerpool.name"="agents"}` |
| Lifecycle operations per second | `sum by ("ate.actor.operation.name") (rate({"ate.actor.lifecycle.operation.duration_count"}[2m]))` |
| Did the scheduler find a free worker? (`no_free_worker` = capacity starvation) | `sum by ("ate.scheduler.outcome") (rate({"ate.scheduler.assignment.duration_count"}[2m]))` |
| How slow is a resume? (P95) | `histogram_quantile(0.95, sum by (le, "ate.actor.operation.name") (rate({"ate.actor.lifecycle.operation.duration_bucket"}[5m])))` |
| Restore time by template | `histogram_quantile(0.95, sum by (le, "ate.template.name") (rate({"ate.actor.restore.duration_bucket"}[5m])))` |
| Where does cold start go? (by phase: download, oci_unpack, ateom_restore, ...) | `histogram_quantile(0.95, sum by (le, "ate.snapshot.phase") (rate({"ate.actor.restore.duration_bucket"}[5m])))` |
| Where does a suspend go? (by phase: ateom_checkpoint, persist, ...) | `histogram_quantile(0.95, sum by (le, "ate.snapshot.phase") (rate({"ate.actor.checkpoint.duration_bucket"}[5m])))` |
| Image cache hit ratio (a miss pays for the pull and unpack) | `sum(rate({"ate.imagecache.requests", "ate.imagecache.outcome"="hit"}[10m])) / sum(rate({"ate.imagecache.requests", "ate.imagecache.outcome"=~"hit\|miss"}[10m]))` |
| Which snapshot kind is being restored? (golden / latest / local) | `sum by ("ate.snapshot.kind") (rate({"ate.actor.restore.duration_count"}[5m]))` |
| Did requests hit a warm actor or trigger a resume? | `sum by ("ate.router.resume") (rate({"atenet.router.route.duration_count", top_level_controller_name="atenet-router"}[2m]))` |
| Router latency, warm vs resumed | `histogram_quantile(0.95, sum by (le, "ate.router.resume") (rate({"atenet.router.route.duration_bucket", top_level_controller_name="atenet-router"}[5m])))` |
| Router requests by outcome (ok, no_capacity, timeout, ...) | `sum by ("ate.router.outcome") (rate({"atenet.router.route.duration_count", top_level_controller_name="atenet-router"}[2m]))` |
| Snapshot image sizes | `histogram_quantile(0.95, sum by (le, "file.name") (rate({"atelet.snapshot.size_bucket"}[10m])))` |
| Actor CPU by template | `sum by ("ate.template.name") (rate({"ate.actor.stats.cpu.time"}[5m]))` |
| Actor memory (working set) by template | `sum by ("ate.template.name") ({"ate.actor.stats.memory.working_set"})` |
| Crashes | `sum(increase({"ate.actor.crashes"}[15m]))` |
| Is the controller keeping up? | `max by ("controller") ({"workqueue_depth"})` |
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

Substrate writes actor identity in two places. Records it writes *on the actor's behalf* (your agent's
stdout, ateom's `Actor restoring` / `Actor checkpointing`, atelet's `Actor usage sample`) carry it in the
label group, `labels."ate.*"`. Records a component writes *about* an actor (ateapi's `Actor state changed`
and `Actor crashed`, atelet's `Restore timing breakdown`) carry the same keys flat in `jsonPayload`. A
query for one actor's whole story needs both.

One actor's history across workers and components, oldest first:
```
resource.type="k8s_container"
(labels."ate.actor.name"="buddy" OR jsonPayload."ate.actor.name"="buddy")
```
Every state change in the atespace (ateapi's record, the authoritative one):
```
resource.type="k8s_container"
jsonPayload.msg="Actor state changed"
jsonPayload."ate.atespace"="workshop"
```
Why was that one resume slow? One record per restore, with every phase in seconds:
```
resource.type="k8s_container"
jsonPayload.msg="Restore timing breakdown"
jsonPayload."ate.actor.name"="buddy"
```
Which actor crashed? (the `ate.actor.crashes` metric can't say, it carries no actor identity):
```
resource.type="k8s_container"
jsonPayload."ate.actor.state"="crashed"
```
Everything one request did, across ateapi, atelet, and ateom. Use the ID `kubectl ate --trace …` prints:
```
resource.type="k8s_container"
jsonPayload.trace_id="<32 hex chars>"
```
Your agent's own stdout (what `kubectl ate logs actor buddy -a workshop` streams):
```
resource.type="k8s_container"
resource.labels.namespace_name="workshop"
labels."ate.actor.name"="buddy"
```
