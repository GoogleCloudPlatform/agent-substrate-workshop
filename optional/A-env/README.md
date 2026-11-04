# Optional lab A — Agent environments with `agent-substrate/env` (30–45 min)

> **Cluster impact:** Namespace-scoped. Adds the `ate-env` namespace, atespace and WorkerPool next to the workshop; nothing in `workshop` changes.

[`agent-substrate/env`](https://github.com/agent-substrate/env) gives an LLM (or you) a **sandboxed shell
environment** that lives in a Substrate actor. You create it, run commands, read and write files, and let it
sleep. It wakes up with its files intact (verify this on your cluster — see the note in step 2). It exposes a
CLI, a Go client, a Python client, and a built-in **MCP** server.

It's the pattern you built in Modules 4–5 (an always-on API in front of suspendable actors), productized.

> **env is alpha and pre-v1.** No tagged release of env (latest: `v0.0.10`) pins a substrate build close to
> ours. This lab instead pins env at a specific commit on its `main` branch — see "Which env commit, and why"
> below for the compatibility check we ran before choosing it.

## Which env commit, and why

Our `SUBSTRATE_REF` is `v1.0.0-rc.1` (pinned in `app/go.mod` as `go.ate.dev/substrate v1.0.0-rc.1`).

We checked every env tag and `main`:

| env ref | substrate pin (`go.mod`) | compatibility with `v1.0.0-rc.1` |
|---|---|---|
| `v0.0.7`–`v0.0.10` | `v0.0.0-20260717234919-a2d55e99e02e` (2026-07-17) | Pre-`v0.2.0` (`readyz`, `snapshotsConfig`) |
| `2c780cd` | `go.ate.dev/substrate v0.5.1-0.20261018212731-ddb46651d605` (2026-10-18) | Matches `v1.0.0-rc.1` with [`patch-env-v060.py`](./patch-env-v060.py) (`ServiceAccount/ate-env-api`, `ate-env manifest access-policy`, `workerSelector` without `sandboxConfig`, `ate.dev/v1` `WorkerPool`, `go.ate.dev/substrate/api/ateapi/v1`, cluster-default snapshot storage location, and integer `cpuMillicores`/`memoryBytes` limits) |

This lab pins `env` at:

```
ENV_REF=2c780cd94891c134cd10375a30a5e1e662ee2c2d   # env, 2026-10-18
```

A small idempotent patch script ([`patch-env-v060.py`](./patch-env-v060.py)) updates the `ateapi`/`v1` imports, `ResourceRequirements`, and `SnapshotConfig` and bumps `go.ate.dev/substrate` to `v1.0.0-rc.1`.

## Two things to know before you start

- **The guest image ships almost nothing.** `ate-env-guest`'s container base is the official `bash` image
  (Alpine + `bash`, from env's `.ko.yaml`). There is no `curl`, `python3`, or `pip` unless you change that
  base image yourself. Step 3 below checks what's actually there and uses `wget` (part of Alpine's busybox)
  instead of `curl` for the egress check. `pip`/PyPI access is still worth locking down for when *you* add a
  Python-capable base image later — hence the policy in step 5.
- **There's no `/work` directory.** The guest's filesystem and process root default to `/` (env's own
  quickstart writes to paths like `/note.txt`), so this lab does the same. Don't assume a scratch directory
  exists unless you create one.

## 1. Build and install (you build every image)

```bash
source scripts/env.sh   # workshop.env into this shell (bash or zsh): KO_DOCKER_REPO, BUCKET_NAME, REPO_ROOT, …
ENV_REF=2c780cd94891c134cd10375a30a5e1e662ee2c2d
git clone https://github.com/agent-substrate/env .workshop/env 2>/dev/null; git -C .workshop/env fetch origin "$ENV_REF" && git -C .workshop/env checkout "$ENV_REF"
cd .workshop/env
python3 "$REPO_ROOT/optional/A-env/patch-env-v060.py" .
go build ./... && go test ./cmd/ate-env/
export KO_DOCKER_REPO
API_IMAGE=$(ko build --base-import-paths ./cmd/ate-env-api)
GUEST_IMAGE=$(ko build --base-import-paths ./cmd/ate-env-guest)
go build -o "$REPO_ROOT/bin/ate-env" ./cmd/ate-env
cd "$REPO_ROOT"

# --replicas 2 matches this workshop's WorkerPool size (module 2); env's own default is 5.
# Pre-create the ate-env atespace and grant ate-env-api's ServiceAccount editor on it (v0.6.0+ enforces
# OpenFGA authorization by default):
kubectl ate create atespace ate-env 2>/dev/null || true
ate-env manifest access-policy | kubectl ate create access-policy -a ate-env -f - 2>/dev/null || true
ate-env manifest --api-image "$API_IMAGE" --worker-image "$(cat .workshop/worker-image)" --replicas 2 \
  | kubectl apply -f -
kubectl -n ate-env rollout status deploy/ate-env-api
ate-env manifest template --guest-image "$GUEST_IMAGE" --snapshots-bucket "gs://$BUCKET_NAME/ate-env/" --cpu 1 --memory 512Mi \
  | kubectl ate create actor-template -f -
kubectl ate get atespaces                                      # "ate-env" is listed next to "workshop"
kubectl ate get actor-template default-template -a ate-env     # repeat until GOLDEN TAG is set (~20s)
```

This creates a Kubernetes namespace `ate-env` (labeled `ate.dev/api-access: "true"`) with its own WorkerPool (`default-template-workerpool`, 2
workers; it sets no Pod CPU or memory limits, so `kubectl ate get workers -n ate-env` shows each worker offering
the whole node, unlike the workshop's 1-CPU workers from Module 5) and the `ate-env-api` Deployment, which plays the role of your gateway: it's the one always-on piece
that proxies to the (suspendable) environment actors. `ate-env` itself is a plain Go binary — no image to
build, no cluster access needed to run it, just an address to the API service.

## 2. Use an environment

```bash
scripts/forward.sh ate-env-api ate-env svc/ate-env-api 7777 7777   # background port-forward; `make stop-forwards` stops it
ate-env create dev1
ate-env dev1 shell 'uname -a; echo hello > /note.txt; ls -la /note.txt'
echo "print('hi from the sandbox')" | ate-env dev1 write /hi.py
ate-env dev1 read /hi.py
ate-env suspend dev1
kubectl ate get actors -a ate-env            # dev1 SUSPENDED
ate-env dev1 shell 'cat /note.txt'       # auto-resumes; verify the file is still there
```

> **Verify on your cluster:** env's `ActorTemplate` (from `manifest template` above) sets
> `snapshotConfig.preferredFidelity: SNAPSHOT_FIDELITY_MEMORY`, so `/note.txt` (in the rootfs overlay) and
> in-memory state survive the suspend even without an explicit `durableDir` volume (compare with Module 3's
> `SNAPSHOT_FIDELITY_MEMORY` vs. `SNAPSHOT_FIDELITY_VOLUMES`).

## 3. Multiplexing

```bash
for i in $(seq 1 10); do ate-env create env$i; ate-env env$i shell "echo env$i > /id"; ate-env suspend env$i; done
kubectl ate get actors -a ate-env
ate-env env7 shell 'cat /id'
```

Ten environments, two workers — the same density story as your swarm in Module 5.

While a couple of these are still resuming, check what's actually in the guest image:

```bash
ate-env dev1 shell 'which curl python3 pip wget sh; cat /etc/os-release | head -2'
```

Expect `curl`, `python3`, and `pip` to come back empty, and `wget` and `sh` to be found — see "Two things to
know before you start" above.

## 4. Drive it over MCP

The API exposes a built-in MCP server at `POST /v1alpha/envs/{id}/mcp` with tools `read_file`, `write_file`,
and `shell` (`internal/mcp/tools.go` at `ENV_REF`; env's top-level README also lists `edit_file`, `glob`,
and `grep`, but those aren't registered at this commit). It uses the MCP **Streamable HTTP** transport,
which requires an `Accept` header naming both response formats it can produce, even for a plain JSON reply:

```bash
curl -s -X POST localhost:7777/v1alpha/envs/dev1/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | jq '.result.tools[].name'
curl -s -X POST localhost:7777/v1alpha/envs/dev1/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"shell","arguments":{"command":"cat /note.txt"}}}' | jq
```

Point any MCP client at that URL (`http://localhost:7777/v1alpha/envs/<id>/mcp`) and your LLM gets a
persistent, sandboxed, suspendable shell plus file tools.

## 5. Lock down egress (env sets no policy)

From the workshop you know an actor with no EgressPolicy has no outbound network at all. Give the sandboxes
just PyPI, the way you'd lock down a real coding-agent sandbox:

```bash
source scripts/ate-api.sh
egressctl -a ate-env apply -f optional/A-env/egress-pypi.yaml --all
ate-env suspend dev1
ate-env dev1 shell '
  wget -q --no-check-certificate -T 5 -O /dev/null https://pypi.org/simple/ && echo "pypi: reachable" || echo "pypi: FAILED (check the policy)"
  wget -q --no-check-certificate -T 5 -O /dev/null https://example.com 2>/dev/null && echo "example.com: reachable (should not happen)" || echo "example.com: denied"
'
```

`wget` stands in for `curl` here for the reason in "Two things to know before you start." Why
`--no-check-certificate`? For `https` hostname rules, the egress gateway terminates TLS and presents a
certificate it minted for `pypi.org`, which chains to the gateway's MITM CA (`egress-mitm.ate.dev`). The env
template doesn't mount that CA ([lab J](../J-credential-injection/README.md)'s step 5 shows how, with a
`systemInfo` trust-bundle volume), so a verifying client stops at `certificate verify failed`.
Either way, what matters is the contrast between the two lines: pypi.org gets through the gateway, and
example.com is refused.

## 6. Find it on the dashboard

Same signals as Module 6, filtered to the `ate-env` atespace instead of `workshop`:

```bash
make prometheus-ui   # http://localhost:9090
```

Paste into the Prometheus UI (or Cloud Console → Monitoring → Metrics Explorer → PromQL):

| Question | PromQL |
|---|---|
| Fleet: worker state on env's pool | `max by ("ate.worker.state") ({"ate.workerpool.workers", "ate.workerpool.name"="default-template-workerpool", cluster="ate-cluster"})` |
| Multiplexing: did ~10 envs ever need more than 2 workers? | `{"ate.workerpool.desired_workers", "ate.workerpool.name"="default-template-workerpool", cluster="ate-cluster"}` vs `{"ate.workerpool.ready_workers", "ate.workerpool.name"="default-template-workerpool", cluster="ate-cluster"}` |
| Suspend/resume latency for envN | `histogram_quantile(0.95, sum by (le, "ate.actor.operation.name") (rate({"ate.actor.lifecycle.operation.duration_bucket", cluster="ate-cluster"}[5m])))` |
| Did `ate-env envN shell` trigger a resume or join a warm one? | `sum by ("ate.router.resume") (rate({"atenet.router.route.duration_count", top_level_controller_name="atenet-router", cluster="ate-cluster"}[5m]))` |

In Cloud Console → Logging → Logs Explorer, the per-actor view from Module 6 works the same way, just with
the atespace swapped (ateapi writes it in `jsonPayload`, not `labels`):
```
resource.type="k8s_container"
resource.labels.cluster_name="ate-cluster"
resource.labels.namespace_name="ate-system"
"Actor state changed"
jsonPayload."ate.atespace"="ate-env"
```
That's the whole `dev1`/`env1`…`env10` fleet suspending and resuming, next to the fleet you already built.
The workshop's own Cloud Monitoring dashboard (`observability/dashboard.json`, applied in Module 0) and
substrate's three dashboards (applied by `make dashboards`) are fixed to the `workshop` atespace and the
`agents` WorkerPool, so they won't show `ate-env` traffic directly — use the ad hoc queries above for this
atespace, the same way you used the Prometheus UI in Module 6 (if your `CLUSTER_NAME` in `workshop.env` is
not `ate-cluster`, replace `ate-cluster` in the queries above).

## Read the code

- `cmd/ate-env/manifest.go`: how env generates its namespace, WorkerPool, `ServiceAccount/ate-env-api`, and API Deployment/Service, plus
  the separate `manifest access-policy` and `manifest template` subcommands. Compare it with your own
  Module 2 manifests — `buildWorkerPool` labels the WorkerPool `ate-env/workerpool: default-template-workerpool`, and `buildActorTemplate` sets the matching `workerSelector` so env's actors run on `default-template-workerpool` rather than the workshop's `agents` pool.
- `internal/ate/client.go`: a second production ateapi client — TLS, a re-read-on-every-call bearer token
  file, and the same `ate-target-actor` router header your gateway sets. Compare its `fileTokenCreds` with
  `app/internal/ateclient`'s `fileToken`: same idea, independently written.

## Clean up

```bash
for e in dev1 $(seq -f 'env%g' 1 10); do ate-env delete "$e"; done
kubectl ate delete actor-template default-template -a ate-env
kubectl ate delete atespace ate-env
ate-env manifest --api-image x --worker-image x | kubectl delete -f - --ignore-not-found
make stop-forwards   # stops step 2's ate-env-api forward (and the workshop's other port-forwards)
```

## Next

Continue to [Optional lab B — Declarative tasks and workspaces with `google/ax`](../B-ax/README.md) (which embeds `env`'s guest gRPC server inside `ax-task-runner` to power `ax ssh`), or back to [Module 7 — Wrap-up](../../modules/07-wrap-up/README.md) for the rest of the optional labs.
