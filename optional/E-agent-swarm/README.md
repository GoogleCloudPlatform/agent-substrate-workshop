# Optional lab E — agent-swarm at 50:2 (30 min)

[agent-swarm](https://github.com/agent-substrate/agent-swarm) is the reference conference demo: 50 attendee
actors (`attendee-01`…`attendee-50`) on a 2-pod WorkerPool (`conf-workers`), with a presenter "stage" UI. Its
gateway uses a **relay** instead of egress: the actor has no egress at all, and instead of calling out itself
it returns a `model_call` step; the gateway calls Vertex and posts the result back through the router.
Compare that with Module 4's gateway-only-egress pattern.

> **Fact-checked against an agent-swarm checkout** (commit `1bdbdb6`, 2026-09-25) — its `README.md`,
> `hack/deploy.sh`, `manifests/workerpool.yaml.tmpl`, and `go.mod`. Two corrections from an earlier draft of
> this lab:
> - The gateway image is built with **`docker buildx`**, not `ko` (`hack/deploy.sh`'s `conf_build_gateway`
>   runs `docker buildx build --platform=linux/amd64 --push`). Only the *worker* image
>   (`ko://github.com/agent-substrate/substrate/cmd/ateom-gvisor`, in `manifests/workerpool.yaml.tmpl`) is
>   `ko`-resolved. You still push everything into your own `KO_DOCKER_REPO` — `hack/deploy.sh` reuses that
>   env var as the target registry for the Docker build too — but you need `docker` with `buildx` installed
>   as well as `ko`.
> - **Keep `--patch-router`.** agent-swarm's `hack/deploy.sh` requires **both** router changes: once the
>   parked-request budget is ≥60s, it also requires `terminationGracePeriodSeconds ≥120` on `atenet-router`,
>   and refuses to deploy otherwise. This workshop's `install-substrate.sh` now sets both (60s budget, 120s
>   grace), but a cluster installed before that change still has substrate's default 60s grace, and
>   `hack/deploy.sh up` then fails with a "needs these changes" error. `--patch-router` covers that case
>   (it patches the grace period too, and says so: "not restored on delete").

## Before you start

agent-swarm refuses to deploy unless your cluster runs the substrate commit pinned in its `go.mod`. At the
commit above, that pin is already `github.com/agent-substrate/substrate v0.0.0-20260918213005-c7a1be177d0f`
— the same build this workshop installs as `SUBSTRATE_REF=c7a1be17`. Re-check before each event, since
agent-swarm's `main` moves:
```bash
git clone https://github.com/agent-substrate/agent-swarm .workshop/agent-swarm
git -C .workshop/agent-swarm checkout 1bdbdb6
grep 'agent-substrate/substrate ' .workshop/agent-swarm/go.mod     # compare with SUBSTRATE_REF in workshop.env
```
If they differ, either bump the pin in a fork (agent-swarm's own README documents "Bumping Substrate") or
install a second cluster at agent-swarm's pin (`SUBSTRATE_REF=<pin> make install` there).

You'll also need `docker` with the `buildx` plugin, on top of the workshop's usual tools.

## Deploy

`load_env` exports `PROJECT_ID` (it's required for the whole workshop), and `hack/deploy.sh` treats "is
`PROJECT_ID` set" as its only mock-vs-Vertex switch — it does **not** read this workshop's `MOCK_LLM`. So by
default this deploys with real Vertex calls through a fresh `conf-gateway` GSA. Run it in a subshell with
`PROJECT_ID` unset if you want mock replies instead:

```bash
source scripts/lib.sh && load_env
cd .workshop/agent-swarm
export KO_DOCKER_REPO BUCKET_NAME VERTEX_LOCATION KO_DEFAULTPLATFORMS=linux/amd64
( export PROJECT_ID; hack/deploy.sh up --patch-router )        # real Vertex calls, or:
( unset PROJECT_ID;  hack/deploy.sh up --patch-router )        # mock replies, no Vertex AI cost
cd "$REPO_ROOT"
source scripts/lib.sh && load_env
port_forward conf-gateway ate-demo-conf svc/conf-gateway 8090 80   # never hand-roll `kubectl port-forward &`
open "http://localhost:8090/stage?key=$(kubectl get secret conf-gateway -n ate-demo-conf -o jsonpath='{.data.PRESENTER_TOKEN}' | base64 -d)"
```
Pick one of the two `hack/deploy.sh up` lines, not both.

## Things to find
- `manifests/actortemplate.yaml.tmpl`: `onPause`/`onCommit` are both `SNAPSHOT_CONTENT_SCOPE_FULL`, and
  there's no `durableDir` at all — conversation history lives only in the actor's checkpointed memory, so a
  suspend/resume (or a worker pod loss) has to bring it back through the FULL snapshot, not a file.
- The stage view's live worker and actor states. Match them against your workshop dashboard queries, just
  pointed at this atespace instead — `observability/dashboard.json` and `observability/queries.md` are fixed
  to `workshop`/`agents`, so use ad hoc PromQL the way [optional lab H](../H-env/README.md) does for
  `ate-env`:

  | Question | PromQL |
  |---|---|
  | Worker state on `conf-workers` | `sum by ("ate.worker.state") ({"ate.workerpool.workers", "ate.workerpool.name"="conf-workers"})` |
  | 50 attendees on 2 workers: desired vs ready | `{"ate.workerpool.desired_workers", "ate.workerpool.name"="conf-workers"}` vs `{"ate.workerpool.ready_workers", "ate.workerpool.name"="conf-workers"}` |
  | Resume latency for `attendee-NN` | `histogram_quantile(0.95, sum by (le, "ate.actor.operation.name") (rate({"ate.actor.lifecycle.operation.duration_bucket"}[5m])))` |

  In Logs Explorer, swap the atespace label the way lab H does:
  `resource.type="k8s_container"`, `"Actor state changed"`, `labels."ate.atespace"="ate-demo-conf"`.
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
