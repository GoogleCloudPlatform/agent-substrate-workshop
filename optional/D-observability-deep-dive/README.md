# Optional lab D — Observability deep dive (30 min)

Starts from the end of Module 6 (`make catch-up MODULE=7`).

> **Fact-checked against the substrate checkout at `SUBSTRATE_REF`** (`docs/metrics/registry/metrics.yaml`,
> `internal/ateattr/ateattr.go`, `cmd/atelet/statsevents.go`) and this repo's own `observability/dashboard.json`
> and `observability/queries.md`. Two corrections from an earlier draft of this lab, called out inline below:
> the resume operation's label value is lowercase `resume`, not `RESUME`; and a logs-based metric over
> per-actor events must stay labeled by template/pool, never by actor name — see step 3.

## 1. Alerts on what the fleet already tells you

Two of these come straight from the canned queries in `observability/queries.md`; the other two are the
signals [Module 6](../../modules/06-observe/README.md) already teaches as the gaps to watch: crashes (the actor
loses everything since its last snapshot until someone runs `kubectl ate revert actor`) and a WorkerPool whose ready workers never catch up with desired.

```bash
source scripts/lib.sh && load_env
for f in alert-actor-crashes alert-workers-desired-vs-ready alert-resume-latency alert-router-503; do
  gcloud monitoring policies create --project "$PROJECT_ID" \
    --policy-from-file="optional/D-observability-deep-dive/$f.json"
done
```

- **`alert-actor-crashes.json`** fires on any `ate.actor.crashes` increase in the workshop atespace. Trigger
  it by crashing an actor — the easiest repeatable way without touching an actor's own code is to delete its
  worker pod out from under it while it's RUNNING (`kubectl -n workshop delete pod -l workload=workshop-agent
  --wait=false` while a chat is in flight) and watch `kubectl ate get actors -a workshop` for `CRASHED`.
  The metric tells you *that* something crashed, not *what*. The alert can't name the actor (that would be
  a series per actor), so go to Logs Explorer:
  ```
  resource.type="k8s_container"
  jsonPayload."ate.actor.state"="crashed"
  ```
  ateapi's `Actor crashed` record names the actor (`ate.actor.name`, `ate.actor.uid`), the operation it
  was in (`ate.actor.operation.name`). For *why*, read the actor itself:
  `kubectl ate get actor NAME -a workshop -o yaml` shows `status.crash.message` and `crashTime` (for the pod
  delete above, "worker pod went away while hosting the actor").
- **`alert-workers-desired-vs-ready.json`** fires when the `agents` WorkerPool's ready workers stay below
  what it asked for, for 5 minutes. It's hard to trigger honestly in the workshop's own 2-replica pool
  without breaking your cluster (over-request node capacity, or drop a quota) — read it as the alert you'd
  actually run in production, per the metric registry's own note: *"the cause is usually an empty node pool,
  a quota, or a worker pod that cannot start."*
- **`alert-resume-latency.json`** and **`alert-router-503.json`** are easier to trigger live. For the second
  one: set `IDLE_AFTER=0s` (`kubectl -n workshop set env deploy/gateway IDLE_AFTER=0s`) — this *disables* the
  idle-suspender (`internal/idle/idle.go`: `IdleAfter <= 0` is a no-op), so nothing frees a worker once it's
  used, and `CONCURRENCY=4 make load` saturates both workers. Parked requests hit the router's 60s budget,
  error turns climb, and the alert fires in about 5–7 min. Restore `IDLE_AFTER=3s` afterwards
  (`kubectl -n workshop set env deploy/gateway IDLE_AFTER=3s`).

## 2. An SLO on chat success
In Cloud Console → Monitoring → SLOs, create a request-based SLO on the gateway:
good = `workshop_chat_turns_total{outcome="ok"}`, total = `workshop_chat_turns_total`, target 99%, rolling 1 day.
Watch the error budget burn during step 1's `IDLE_AFTER=0s` run.

## 3. Usage events as a logs-based metric — labeled by template/pool, not actor
Every worker emits an `"Actor usage sample"` log line per actor per poll tick (atelet's
`cmd/atelet/statsevents.go`) — CPU, memory, and identity, but *as a log*, not a metric series, specifically
**because** it carries actor identity. Module 6 already made this point for `ate.template.name`-labeled
metrics; the same rule applies when you build your own logs-based metric from these events: keep actor
identity in the log filter you eyeball, but never promote it to a metric label.

```bash
gcloud logging metrics create actor_usage_events --project "$PROJECT_ID" \
  --description="Actor usage samples, labeled only by template and pool (never actor — cardinality)" \
  --log-filter='resource.type="k8s_container" AND jsonPayload.msg="Actor usage sample"'
```
In the metric's settings (Logs Explorer → your new metric → "Convert to a metric" panel, or Metrics Explorer
→ edit), add label extractors for `labels."ate.template.name"` and `labels."ate.workerpool.name"` — **not**
`labels."ate.actor.name"`. Chart it in Metrics Explorer grouped by template: this tells you which *template*
generates the most usage events under `make load`, without ever creating a per-actor time series.

To drill into one actor's own usage events (a log search, not a metric — no cardinality limit applies here),
use Logs Explorer directly:
```
resource.type="k8s_container"
jsonPayload.msg="Actor usage sample"
labels."ate.actor.name"="swarm-01"
```

## 4. Extend the workshop dashboard
Add a tile for worker utilization — awake actors (gateway) against desired workers (substrate) for the
`agents` pool — to `observability/dashboard.json`, then run `make dashboards`. It updates in place.
`bash scripts/test/dashboards_test.sh` lints your edit (12-column grid, every tile has a title, dotted names
quoted, at least 10 queries).

```json
{
  "xPos": 0, "yPos": 36, "width": 6, "height": 4,
  "widget": {
    "title": "Worker utilization (awake actors / desired workers, pool agents)",
    "xyChart": {
      "dataSets": [{
        "timeSeriesQuery": {"prometheusQuery": "workshop_actors_awake / on() group_left max({\"ate.workerpool.desired_workers\", \"ate.workerpool.name\"=\"agents\"})"},
        "plotType": "LINE", "legendTemplate": "utilization"
      }],
      "yAxis": {"label": "ratio", "scale": "LINEAR"}
    }
  }
}
```
Add it as a new element of `mosaicLayout.tiles` (the dashboard's last tile currently ends at `yPos: 32`,
`height: 4`, so `yPos: 36` is the next free row).

## 5. Traces
`kubectl ate` propagates a trace context on request, but doesn't export its own spans anywhere — it's
substrate's server-side components (ateapi, atenet-router, atelet) that export OTLP through the GKE managed
collector into Cloud Trace. Enable it per-command with `--trace`, and it prints the trace ID it generated so
you can find that exact call in Cloud Trace:

```bash
kubectl ate --trace resume actor buddy -a workshop
# stderr: Tracing enabled. Trace ID: <32 hex chars>
```
(If `buddy` is already RUNNING, suspend it first so the resume actually happens:
`kubectl ate suspend actor buddy -a workshop`.)

Open Cloud Trace → Trace Explorer, paste the printed trace ID into "Find a specific trace", and look at the
span breakdown for that one resume: ateapi's steps (`step.AssignWorker`, `step.CallAteletRestore`, …),
atelet's snapshot download and OCI prep, and an `ateom-gvisor` span. That last one is tagged
`ate.otlp.relay=relay`: ateom runs inside the sandbox with no network of its own, so atelet relays its
OTLP out for it. Data plane traffic (the router forwarding your actual chat request) is sampled at 1%, so
this per-command trace ID is the reliable way to find *a specific* call rather than hoping a resume shows
up in a random sample.

Spans carry actor identity too. In Trace Explorer, filter on the span attribute `ate.actor.name` = `buddy`
to list every traced operation on that actor, including failed ones.

**Join the trace to its logs.** Every Substrate log record written while handling that request carries
the same ID as a top-level `trace_id` field (Cloud Logging doesn't link them for you, since the entry's
own `trace` field is left empty). In Logs Explorer:
```
resource.type="k8s_container"
jsonPayload.trace_id="<the ID the CLI printed>"
```
You get the request across three components: ateapi (`Picked worker`, the lease, `Actor state changed`),
atelet (`Image cache hit`, `Restore timing breakdown`), and ateom (`Actor restoring`, `Actor restored`).
Records from untraced requests carry a `trace_id` too, with `trace_flags` `00`: not sampled, so no spans
exist for it.

**Where did the restore time go?** Open the `Restore timing breakdown` record. Its
`ate.actor.restore.duration.*` fields split the resume into phases (`total` plus each step), and
`ate.snapshot.kind` says which snapshot it came from. To compare resumes of one actor:
```
resource.type="k8s_container"
jsonPayload.msg="Restore timing breakdown"
jsonPayload."ate.actor.name"="buddy"
```

## Clean up
```bash
gcloud monitoring policies list --project "$PROJECT_ID" --filter='displayName~"^Workshop:"' \
  --format='value(name)' | xargs -r -n1 gcloud monitoring policies delete --project "$PROJECT_ID" --quiet
gcloud logging metrics delete actor_usage_events --project "$PROJECT_ID" --quiet
```

## Next
Back to [Module 7 — Wrap-up](../../modules/07-wrap-up/README.md) for the rest of the optional labs.
