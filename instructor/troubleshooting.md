# Troubleshooting

Each entry has a **symptom**, its **cause**, and a **fix**. Attendees who are far behind can run
`make catch-up MODULE=N`, which reproduces the verified end state of module `N-1`.

## Install

| Symptom | Cause | Fix |
|---|---|---|
| Install hangs waiting for pods; ateapi pods stuck in `ContainerCreating` with a ClusterTrustBundle mount error | The cluster doesn't serve `certificates.k8s.io/v1beta1` ClusterTrustBundles (GKE < 1.37 without beta APIs) | Recreate the cluster at 1.37+, or 1.36 with `--enable-kubernetes-unstable-apis=certificates.k8s.io/v1beta1/clustertrustbundles,certificates.k8s.io/v1beta1/podcertificaterequests`. `make preflight` checks this before anyone waits on it. |
| Worker Pods never become `Ready`, or actor create/resume fails with obscure API/schema errors | The WorkerPool's `workerImage` isn't `ateom-gvisor` from the **same build** as the control plane (`ate-setup`, `kubectl-ate`). This can't happen through `make install`, which pins both from one `install-substrate.sh` run — but it can happen if someone hand-edits a WorkerPool, or changes `SUBSTRATE_REF` without reinstalling. | Always render the WorkerPool with `scripts/render.sh` (it reads `.workshop/worker-image`, never a hand-typed tag). After changing `SUBSTRATE_REF`, rerun `make install` before re-applying the WorkerPool — that's what writes a new `.workshop/worker-image` to match. |
| Worker pods `Pending` with `node(s) didn't match node selector` | Nodes lack the `ate.dev/substrate-version` label, e.g. after a node pool recreate (this is also why the runbook says no node auto-upgrade and no spot nodes: either can silently swap in an unlabeled node) | Rerun `make install` — `install-substrate.sh` labels every node that's missing `ate.dev/substrate-version`. |
| `ko build` fails with 403 on push | Docker credential helper not configured for Artifact Registry | `gcloud auth configure-docker ${REGION}-docker.pkg.dev` (this is also step 3 of `make gcp`, so re-running `make gcp` fixes it too). |

## Actors

| Symptom | Cause | Fix |
|---|---|---|
| Template never gets a `goldenTag` | No free worker for the golden actor — both workers are running other actors | Suspend an actor first (e.g. `kubectl ate suspend actor hello -a workshop`). Every module that creates a new template suspends the previous one for exactly this reason; `catch-up.sh` does the same. |
| Template create rejected: image must be digest-pinned | The image ref has only a tag, not `@sha256:…` (a CEL admission rule enforces this) | Use `scripts/render.sh`, which reads `.workshop/agent-image` — always a digest-pinned ref written by `make build-agent`/`ko build`. |
| `curl` through the router returns `503` after about 60s | All workers are busy and nothing freed one within the parked-request budget (`atenet-router --parked-request-budget=60s`, raised from the substrate default of 5s by `make install`) | Expected in Module 5 step 4 — that's the demo. Otherwise, suspend an actor, or turn on the gateway's idle-suspender (`kubectl -n workshop set env deploy/gateway IDLE_AFTER=20s`). |
| `curl` returns `503` immediately, or times out at 5s | The router's `--parked-request-budget` patch never landed, so it's still substrate's 5s default | `make verify MODULE=1` checks for the patched flag and fails if it's missing. Rerun `make install`. |
| Actor state `CRASHED` | The agent process exited, or failed `readyz` after a restore | `kubectl ate logs actor NAME -a workshop` to see why. Substrate won't resume a `CRASHED` actor on its own: `kubectl ate delete actor NAME -a workshop --any-state`, then recreate it. |
| `disk=-1` in Module 3 | `DataDir` isn't set in `main.go`, or the image wasn't rebuilt before creating `agent-v2` | Fix `main.go`, `make build-agent`, then create a new template version (e.g. `agent-v2b` — a template's golden snapshot is immutable, so you can't edit `agent-v2` in place). Or just `make catch-up MODULE=4`, which redoes both from the workshop's own known-good solution. |

## Gateway and egress

| Symptom | Cause | Fix |
|---|---|---|
| Chat returns `502 model call failed` | Deny-by-default egress: the actor has no `EgressPolicy` at all — no policy means no tunnel, not even a denied one | Apply one: `make egress POLICY=modules/04-make-it-an-agent/manifests/egress-gateway.yaml ACTOR=buddy`. |
| Chat still returns `502` for a few seconds right after applying the policy | `atenet-egress` caches each actor's policy for up to `DefaultPolicyCacheTTL` (10s in substrate's router), so a request that lands right after `apply` can still see the old "no policy" answer | Just retry (once, a few seconds later). **No suspend or resume is needed** — the policy reaches a *running* actor's next outbound call on its own; Module 4 step 4 calls this out explicitly, and `scripts/catch-up.sh` retries once after a short sleep for the same reason. |
| `make egress` fails with `Unauthenticated` or a TLS error | An expired token, or a stale CA bundle cached under `.workshop/` | Just rerun it — `scripts/ate-api.sh` (sourced by `scripts/egress.sh`) mints a fresh token and re-fetches the ClusterTrustBundle on every run. |
| `/v1/model` returns `"backend": "mock-fallback"` | The Vertex call failed — almost always a 403 while the gateway's Workload Identity binding (`roles/aiplatform.user`) is still propagating, or missing | Check `kubectl -n workshop logs deploy/gateway`. Rerun `make gcp` (idempotent — it's safe to re-run the IAM bindings), then wait 1–2 minutes. To keep going without Vertex in the meantime: `kubectl -n workshop set env deploy/gateway MOCK_LLM=true`. |
| Gateway logs `idle: list actors: … PermissionDenied` / `Unauthenticated` | The projected service-account token's audience, or the ClusterTrustBundle mount, is wrong — or ClusterTrustBundle projection isn't supported on this cluster | Check the `ate-token`/`ate-trust` volumes on the gateway Deployment (`modules/04-make-it-an-agent/manifests/gateway.yaml.tmpl`). As a last resort for a workshop demo, `ATE_API_INSECURE=true` on the gateway skips TLS verification to ateapi. |
| Chats hang, or every request times out, after `kubectl -n workshop set env deploy/gateway IDLE_AFTER=…` | Setting `IDLE_AFTER` rolls the gateway Deployment, which kills any port-forward pointed at the old Pod | Just run `make forward-gateway` again — `port_forward` (in `scripts/lib.sh`) notices the old process is dead and starts a new one; you don't need to `pkill` it yourself first. |
| **Substrate never suspends an actor on its own** | By design — only your app knows what "idle" means for it | This isn't a bug to fix; it's Module 5's point. The gateway's idle-suspender (`IDLE_AFTER`, `app/internal/idle`) is the workshop's answer, and it's off until you turn it on. |

## Observability

| Symptom | Cause | Fix |
|---|---|---|
| Queries return nothing | Metrics lag 1–3 minutes behind the traffic that produced them | Wait, and keep `make load` running in a second terminal so there's always fresh traffic. |
| Substrate's platform metrics are empty, but the gateway's own metrics show data | Managed OpenTelemetry collection isn't enabled on the cluster | `make preflight` flags this. Fix: `gcloud container clusters update $CLUSTER --managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS` (the same command `make preflight` prints). |
| Prometheus UI shows `403` / `permission denied` | The `prometheus-ui` KSA lacks `roles/monitoring.viewer` (Workload Identity) | Rerun `make gcp` to (re-)bind the role, then `kubectl -n workshop rollout restart deploy/prometheus-ui`. |
| PromQL parse error on a metric or label name | Substrate's OTel names are dotted (e.g. `ate.workerpool.workers`) and need quoting in PromQL | Quote it: `{"ate.workerpool.workers"}`, `sum by ("ate.worker.state") (...)`. See [observability/queries.md](../observability/queries.md) for the canned versions. |
| Cloud Console dashboard is empty even though the Prometheus UI shows data | The workshop dashboard wasn't created, or was created against a different project | `make dashboards` is idempotent (matches by `displayName`) — just rerun it. |
