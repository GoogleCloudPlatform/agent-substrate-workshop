# Canned queries

Paste these into the Prometheus UI (`make prometheus-ui` -> http://localhost:9090) or into
Cloud Console -> Monitoring -> Metrics Explorer -> PromQL. Substrate's metric and label names
contain dots, so they are **quoted** (UTF-8 PromQL). Points arrive 1-3 minutes after the action.

Every query is pinned to `cluster="ate-cluster"`, the default `CLUSTER_NAME`. Managed Prometheus is per
*project*, so without it a query also counts any other cluster reporting there (your instructor's, or a
second cluster of your own). If your `CLUSTER_NAME` is different, change that matcher; the dashboards
(`make dashboards`) fill in your `CLUSTER_NAME` for you.
Never `kubectl port-forward` by hand for this — `make prometheus-ui`, `make forward-gateway`, and
`make forward-router` set up (and reuse) the port-forwards for you.

## The fleet (substrate)

| Question | PromQL |
|---|---|
| How many workers, in which state (`idle`, `partial`, `at_capacity`, `unschedulable`)? Every ateapi replica reports the whole pool, so take `max`, not `sum`. A worker running one of our actors is `partial` (actor slots, not CPU) | `max by ("ate.worker.state") ({"ate.workerpool.workers", "ate.workerpool.name"="agents", cluster="ate-cluster"})` |
| Desired vs ready workers | `{"ate.workerpool.desired_workers", "ate.workerpool.name"="agents", cluster="ate-cluster"}` and `{"ate.workerpool.ready_workers", "ate.workerpool.name"="agents", cluster="ate-cluster"}` |
| Lifecycle operations per second | `sum by ("ate.actor.operation.name") (rate({"ate.actor.lifecycle.operation.duration_count", cluster="ate-cluster"}[2m]))` |
| Did the scheduler find a free worker? (`no_capacity` = capacity starvation) | `sum by ("ate.scheduler.outcome") (rate({"ate.scheduler.assignment.duration_count", cluster="ate-cluster"}[2m]))` |
| How slow is a resume? (P95) | `histogram_quantile(0.95, sum by (le, "ate.actor.operation.name") (rate({"ate.actor.lifecycle.operation.duration_bucket", cluster="ate-cluster"}[5m])))` |
| Restore time by template | `histogram_quantile(0.95, sum by (le, "ate.template.name") (rate({"ate.actor.restore.duration_bucket", cluster="ate-cluster"}[5m])))` |
| Where does cold start go? (by phase: download, oci_unpack, ateom_restore, ...) | `histogram_quantile(0.95, sum by (le, "ate.snapshot.phase") (rate({"ate.actor.restore.duration_bucket", cluster="ate-cluster"}[5m])))` |
| Where does a suspend go? (by phase: ateom_checkpoint, persist, ...) | `histogram_quantile(0.95, sum by (le, "ate.snapshot.phase") (rate({"ate.actor.checkpoint.duration_bucket", cluster="ate-cluster"}[5m])))` |
| Image cache hit ratio (a miss pays for the pull and unpack) | `sum(rate({"ate.imagecache.requests", "ate.imagecache.outcome"="hit", cluster="ate-cluster"}[10m])) / sum(rate({"ate.imagecache.requests", "ate.imagecache.outcome"=~"hit\|miss", cluster="ate-cluster"}[10m]))` |
| Which snapshot kind is being restored? (golden / latest / local) | `sum by ("ate.snapshot.kind") (rate({"ate.actor.restore.duration_count", cluster="ate-cluster"}[5m]))` |
| Did requests hit a warm actor or trigger a resume? | `sum by ("ate.router.resume") (rate({"atenet.router.route.duration_count", top_level_controller_name="atenet-router", cluster="ate-cluster"}[2m]))` |
| Router latency, warm vs resumed | `histogram_quantile(0.95, sum by (le, "ate.router.resume") (rate({"atenet.router.route.duration_bucket", top_level_controller_name="atenet-router", cluster="ate-cluster"}[5m])))` |
| Router requests by outcome (ok, no_capacity, timeout, ...) | `sum by ("ate.router.outcome") (rate({"atenet.router.route.duration_count", top_level_controller_name="atenet-router", cluster="ate-cluster"}[2m]))` |
| Snapshot image sizes by template (swap in `"file.name"` to see which file: gVisor's `pages.img`, a microVM's `memory-ranges`) | `histogram_quantile(0.95, sum by (le, "ate.template.name") (rate({"atelet.snapshot.size_bucket", cluster="ate-cluster"}[10m])))` |
| Actor CPU by template | `sum by ("ate.template.name") (rate({"ate.actor.stats.cpu.time", cluster="ate-cluster"}[5m]))` |
| Actor memory (working set) by template | `sum by ("ate.template.name") ({"ate.actor.stats.memory.working_set", cluster="ate-cluster"})` |
| Crashes so far (a step up = a new crash) | `sum({"ate.actor.crashes", cluster="ate-cluster"}) or vector(0)` (0 on a healthy fleet, not "no data"). The running total, not `increase()`: each template's series is born at 1 on its first crash, and `increase()` needs a sample before that, so it misses exactly that crash. The total drops back when ateapi restarts |
| Is the controller keeping up? | `max by ("controller") ({"workqueue_depth", cluster="ate-cluster"})` |
| ateapi gRPC errors | `sum by ("rpc.method") (rate({"rpc.server.call.duration_count", top_level_controller_name="ate-api-server", "rpc.response.status_code"!="OK", cluster="ate-cluster"}[5m]))` |

## Your app (gateway)

| Question | PromQL |
|---|---|
| Chat turns by outcome | `sum by (outcome) (rate(workshop_chat_turns_total{cluster="ate-cluster"}[2m]))` |
| Model latency by backend | `histogram_quantile(0.95, sum by (le, backend) (rate(workshop_model_request_duration_seconds_bucket{cluster="ate-cluster"}[5m])))` |
| Tokens per minute | `sum by (direction) (rate(workshop_model_tokens_total{cluster="ate-cluster"}[5m])) * 60` |
| Awake vs tracked actors | `max(workshop_actors_awake{cluster="ate-cluster"})` / `max(workshop_actors_tracked{cluster="ate-cluster"})` |
| Idle suspends | `sum by (outcome) (increase(workshop_idle_suspends_total{cluster="ate-cluster"}[5m]))` |

## Logs (Cloud Console -> Logging -> Logs Explorer)

Substrate writes actor identity in two places. Records it writes *on the actor's behalf* (your agent's
stdout, ateom's `Actor restoring` / `Actor checkpointing`) carry it in the
label group, `labels."ate.*"`. Records a component writes *about* an actor (ateapi's `Actor state changed`
and `Actor crashed`, atelet's `Restore timing breakdown`, ateom's `Actor usage sampled`) carry the same keys flat in `jsonPayload`. A
query for one actor's whole story needs both.

One actor's history across workers and components, oldest first:
```
resource.type="k8s_container"
resource.labels.cluster_name="ate-cluster"
(labels."ate.actor.name"="buddy" OR jsonPayload."ate.actor.name"="buddy")
```
Every state change in the atespace (ateapi's record, the authoritative one):
```
resource.type="k8s_container"
resource.labels.cluster_name="ate-cluster"
jsonPayload.msg="Actor state changed"
jsonPayload."ate.atespace"="workshop"
```
Why was that one resume slow? One record per restore, with every phase in seconds:
```
resource.type="k8s_container"
resource.labels.cluster_name="ate-cluster"
jsonPayload.msg="Restore timing breakdown"
jsonPayload."ate.actor.name"="buddy"
```
Which actor crashed? (the `ate.actor.crashes` metric can't say, it carries no actor identity):
```
resource.type="k8s_container"
resource.labels.cluster_name="ate-cluster"
jsonPayload."ate.actor.state"="crashed"
```
Everything one request did, across ateapi, atelet, and ateom. Use the ID `kubectl ate --trace …` prints:
```
resource.type="k8s_container"
resource.labels.cluster_name="ate-cluster"
jsonPayload.trace_id="<32 hex chars>"
```
Your agent's own stdout (what `kubectl ate logs actor buddy -a workshop` streams):
```
resource.type="k8s_container"
resource.labels.cluster_name="ate-cluster"
resource.labels.namespace_name="workshop"
labels."ate.actor.name"="buddy"
```
