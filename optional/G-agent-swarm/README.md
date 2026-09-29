# Optional lab G — agent-swarm at 50:20 (30 min)

> **Cluster impact:** Cluster-wide, with a clean-up. Claims the `external.metrics.k8s.io` APIService (run [lab F](../F-hpa-workerpool/README.md)'s clean-up first), patches `atenet-router` flags that aren't restored, and needs ~11.5 GiB of node memory at full scale. Run its clean-up before lab H.

[agent-swarm](https://github.com/moficodes/agent-swarm) is the reference conference demo: 50 attendee
actors (`attendee-01`…`attendee-50`) on an autoscaled WorkerPool (`conf-workers`, 2 to 5 pods), with a
presenter "stage" UI. Its gateway uses a **relay** instead of egress: the actor has no egress at all, and
instead of calling out itself it returns a `model_call` step; the gateway calls Vertex and posts the result
back through the router. Compare that with Module 4's gateway-only-egress pattern.

Each `conf-workers` pod hosts `DEMO_CONF_ACTORS_PER_WORKER` actors at once (default 4). `hack/deploy.sh` sets
the worker's CPU limit to that many CPUs plus 500m, and each actor asks for 1 CPU, so the limit decides the
seat count (the shared-budget rule from [Module 5](../../modules/05-density-idle/README.md)). At the default
maximum of 5 workers, 20 of the 50 attendees run at once and the rest wait suspended. An HPA scales the pool
on worker demand: workers that are `partial` or `at_capacity`, plus one worker for every
`DEMO_CONF_ACTORS_PER_WORKER` requests parked at the router.

> **Fact-checked against an agent-swarm checkout** (commit `7f22bb5`, 2026-09-28) — its `README.md`,
> `hack/deploy.sh`, `manifests/workerpool.yaml.tmpl`, `manifests/actortemplate.yaml.tmpl`, and `go.mod`:
> - agent-swarm pins `github.com/agent-substrate/substrate v0.2.0`, the same as this workshop's
>   `SUBSTRATE_REF`. `hack/deploy.sh` requires the nodes' `ate.dev/substrate-version` label to equal that
>   pin, and `make install` labels them `v0.2.0`, so it deploys onto your workshop cluster.
> - The gateway image is built with **`docker buildx`**, not `ko` (`hack/deploy.sh`'s `conf_build_gateway`
>   runs `docker buildx build --platform=linux/amd64 --push`). Only the *worker* image
>   (`ko://github.com/agent-substrate/substrate/cmd/ateom-gvisor`, in `manifests/workerpool.yaml.tmpl`) is
>   `ko`-resolved. You still push everything into your own `KO_DOCKER_REPO` — `hack/deploy.sh` reuses that
>   env var as the target registry for the Docker build too — but you need `docker` with `buildx` installed
>   as well as `ko`.
> - **Keep `--patch-router`.** agent-swarm's `hack/deploy.sh` requires a `--parked-request-budget` of at least
>   60s, any `--route-timeout` at least 60s, and `terminationGracePeriodSeconds` of at least 120 on
>   `atenet-router`, and refuses to deploy otherwise. This workshop's `install-substrate.sh` sets the budget
>   (60s) and the grace period (120s), but a cluster installed before that change still has substrate's
>   default 60s grace, and `hack/deploy.sh up` then fails with a "needs these changes" error.
>   `--patch-router` covers that case (it sets all three, and says so: "not restored on delete").

## Before you start

agent-swarm refuses to deploy unless your cluster runs the substrate version pinned in its `go.mod`. Its
`main` moves, so re-check the pin against `SUBSTRATE_REF` before each event:
```bash
git clone https://github.com/moficodes/agent-swarm .workshop/agent-swarm
git -C .workshop/agent-swarm checkout 7f22bb5
grep 'agent-substrate/substrate ' .workshop/agent-swarm/go.mod     # expect v0.2.0, the SUBSTRATE_REF in workshop.env
```
If they differ, `hack/deploy.sh up` stops and prints the pin and the cluster's version (agent-swarm's
README has a "Bumping Substrate" section). Use an agent-swarm commit whose pin matches rather than bumping
either side on the day.

Skip agent-swarm's `hack/create-cluster.sh` and its one-time `setup-gcp` project setup. Your workshop
cluster and `make gcp` already cover both: GKE 1.37 with the ClusterTrustBundle beta API, an upgrade freeze,
the snapshot bucket, a registry, and the Workload Identity bindings.

You'll also need `docker` with the `buildx` plugin, on top of the workshop's usual tools.

Two things to know about sharing the cluster with the workshop:
- agent-swarm installs prometheus-adapter as the cluster's `v1beta1.external.metrics.k8s.io` APIService, and
  refuses to run if another adapter (the Stackdriver adapter, KEDA, or [lab F](../F-hpa-workerpool/README.md)'s
  prometheus-adapter) already serves it. If you did lab F, run its clean-up first.
- Its workers land on the same two nodes as the workshop's `agents` pool. At the default 4 seats, each
  `conf-workers` pod requests 500m CPU and 2304Mi memory (512Mi per seat plus 256Mi), so 5 of them take
  about 11.5 GiB. If pods stay `Pending`, run `make teardown` first or lower `DEMO_CONF_MAX_WORKERS`.

## Deploy

`load_env` exports `PROJECT_ID` (it's required for the whole workshop), and `hack/deploy.sh` treats "is
`PROJECT_ID` set" as its only mock-vs-Vertex switch — it does **not** read this workshop's `MOCK_LLM`. So by
default this deploys with real Vertex calls through a fresh `conf-gateway` GSA. Run it in a subshell with
`PROJECT_ID` unset if you want mock replies instead:

```bash
source scripts/lib.sh && load_env
cd .workshop/agent-swarm
export KO_DOCKER_REPO BUCKET_NAME VERTEX_LOCATION KO_DEFAULTPLATFORMS=linux/amd64
# Optional, with defaults: DEMO_CONF_ACTORS=50 DEMO_CONF_MIN_WORKERS=2 DEMO_CONF_MAX_WORKERS=5 DEMO_CONF_ACTORS_PER_WORKER=4
( export PROJECT_ID; hack/deploy.sh up --patch-router )        # real Vertex calls, or:
( unset PROJECT_ID;  hack/deploy.sh up --patch-router )        # mock replies, no Vertex AI cost
cd "$REPO_ROOT"
source scripts/lib.sh && load_env
port_forward conf-gateway ate-demo-conf svc/conf-gateway 8090 80   # never hand-roll `kubectl port-forward &`
open "http://localhost:8090/stage?key=$(kubectl get secret conf-gateway -n ate-demo-conf -o jsonpath='{.data.PRESENTER_TOKEN}' | base64 -d)"
```
`?key=` sets a cookie and redirects (303) to `/stage`, which is what a browser wants. From `curl`, send the key
as a header instead: `curl -H "X-Presenter-Key: $KEY" localhost:8090/stage`. After a `hack/deploy.sh down`
and `up`, restart the port-forward: the Service's pods changed.
Pick one of the two `hack/deploy.sh up` lines, not both.

## Things to find
- `manifests/actortemplate.yaml.tmpl`: `onPause`/`onCommit` are both `SNAPSHOT_CONTENT_SCOPE_FULL`, and
  there's no `durableDir` at all — conversation history lives only in the actor's checkpointed memory, so a
  suspend/resume (or a worker pod loss) has to bring it back through the FULL snapshot, not a file.
- The stage header. It shows the pod count ("3 of 5", or "scaling N → M" while the HPA resizes the pool),
  the seat count (pods × seats per pod), and the oversubscription ratio (attendees ÷ seats), next to the
  turns parked at the router. Each worker card lists its seats and the actor in each. Chat from a few
  attendee slots at once and watch the pool grow with `kubectl -n ate-demo-conf get hpa conf-workers -w`.
  It grows within about 20s of a burst and shrinks 5 minutes after the burst ends.
- The stage view's live worker and actor states. Match them against your workshop dashboard queries, just
  pointed at this atespace instead — `observability/dashboard.json` and `observability/queries.md` are fixed
  to `workshop`/`agents`, so use ad hoc PromQL the way [optional lab A](../A-env/README.md) does for
  `ate-env`:

  | Question | PromQL |
  |---|---|
  | Worker state on `conf-workers` | `max by ("ate.worker.state") ({"ate.workerpool.workers", "ate.workerpool.name"="conf-workers"})` |
  | 50 attendees on 2–5 workers: desired vs ready | `{"ate.workerpool.desired_workers", "ate.workerpool.name"="conf-workers"}` vs `{"ate.workerpool.ready_workers", "ate.workerpool.name"="conf-workers"}` |
  | Resume latency for `attendee-NN` | `histogram_quantile(0.95, sum by (le, "ate.actor.operation.name") (rate({"ate.actor.lifecycle.operation.duration_bucket", "ate.template.name"="conf-agent"}[5m])))` |

  Managed Prometheus is per project, not per cluster. If another cluster in your project also runs
  substrate, add `"cluster"="<your CLUSTER_NAME>"` inside each selector, or the numbers mix both clusters.

  In Logs Explorer, swap the atespace label the way lab A does:
  `resource.type="k8s_container"`, `"Actor state changed"`, `jsonPayload."ate.atespace"="ate-demo-conf"` (ateapi writes the atespace in `jsonPayload`, not `labels`).
- `substrate-gaps.md`: the maintainers' list of what Substrate doesn't do yet — worth comparing against
  [Module 7](../../modules/07-wrap-up/README.md#limits-to-know-before-you-build)'s own limits list.

## Clean up
```bash
cd .workshop/agent-swarm
hack/deploy.sh down
cd "$REPO_ROOT" && source scripts/lib.sh && stop_port_forwards
```
`hack/deploy.sh down` leaves the router flags and the `conf-gateway` service account's IAM bindings in
place — it prints the `gcloud` commands to remove them if you want them gone.

## Next
Back to [Module 7 — Wrap-up](../../modules/07-wrap-up/README.md) for the rest of the optional labs.
