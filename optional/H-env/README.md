# Optional lab H — Agent environments with `agent-substrate/env` (30–45 min)

[`agent-substrate/env`](https://github.com/agent-substrate/env) gives an LLM (or you) a **sandboxed shell
environment** that lives in a Substrate actor. You create it, run commands, read and write files, and let it
sleep. It wakes up with its files intact (verify this on your cluster — see the note in step 2). It exposes a
CLI, a Go client, a Python client, and a built-in **MCP** server.

It's the pattern you built in Modules 4–5 (an always-on API in front of suspendable actors), productized.

> **env is alpha and pre-v1.** No tagged release of env (latest: `v0.0.10`) pins a substrate build close to
> ours. This lab instead pins env at a specific commit on its `main` branch — see "Which env commit, and why"
> below for the compatibility check we ran before choosing it.

## Which env commit, and why

Our `SUBSTRATE_REF` is `c7a1be17` (pinned in `app/go.mod` as
`github.com/agent-substrate/substrate v0.0.0-20260918213005-c7a1be177d0f`, i.e. a build from 2026-09-18).

We checked every env tag and `main`:

| env ref | substrate pin (`go.mod`) | age vs. our pin |
|---|---|---|
| `v0.0.7`–`v0.0.10` | `v0.0.0-20260717234919-a2d55e99e02e` (2026-07-17) | ~2 months older |
| `main` @ `ab40c7b` | `v0.0.0-20260909202336-0b3d2d078f64` (2026-09-09) | 9 days older |

No ref matches exactly, so this lab pins:

```
ENV_REF=ab40c7bfb2049af1a7aade9e7bf9c6cac925b5ca   # env main, 2026-09-11
```

**Compatibility risk:** `ab40c7b` is an untagged commit on a fast-moving `main`, not a release, so it can
change under you if you re-clone without pinning the SHA. As a sanity check we bumped a scratch checkout of
`ab40c7b`'s `go.mod` to our exact `SUBSTRATE_REF` (`go get github.com/agent-substrate/substrate@c7a1be177d0f`)
and ran `go build ./...` and `go test ./...`: both passed with no code changes. That's compile- and
unit-test-level evidence only — it doesn't prove wire compatibility with the exact ateapi/atenet your cluster
runs. If your `SUBSTRATE_REF` has since moved, redo this check before trusting the pin.

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
source scripts/lib.sh && load_env
ENV_REF=ab40c7bfb2049af1a7aade9e7bf9c6cac925b5ca
git clone https://github.com/agent-substrate/env .workshop/env 2>/dev/null; git -C .workshop/env checkout "$ENV_REF"
cd .workshop/env
export KO_DOCKER_REPO
API_IMAGE=$(ko build --base-import-paths ./cmd/ate-env-api)
GUEST_IMAGE=$(ko build --base-import-paths ./cmd/ate-env-guest)
go build -o "$REPO_ROOT/bin/ate-env" ./cmd/ate-env
cd "$REPO_ROOT"

# --replicas 2 matches this workshop's WorkerPool size (module 2); env's own default is 5.
ate-env manifest --api-image "$API_IMAGE" --worker-image "$(cat .workshop/worker-image)" --replicas 2 \
  | kubectl apply -f -
kubectl -n ate-env rollout status deploy/ate-env-api
ate-env manifest template --guest-image "$GUEST_IMAGE" --snapshots-bucket "gs://$BUCKET_NAME/ate-env/" \
  | kubectl ate create actor-template -f -
kubectl ate get atespaces        # an "ate-env" atespace now exists — env creates it on first use
```

This creates a Kubernetes namespace `ate-env` with its own WorkerPool (`default-template-workerpool`, 2
workers) and the `ate-env-api` Deployment, which plays the role of your gateway: it's the one always-on piece
that proxies to the (suspendable) environment actors. `ate-env` itself is a plain Go binary — no image to
build, no cluster access needed to run it, just an address to the API service.

## 2. Use an environment

```bash
port_forward ate-env-api ate-env svc/ate-env-api 7777 7777   # from scripts/lib.sh, sourced in step 1
ate-env create dev1
ate-env dev1 shell 'uname -a; echo hello > /note.txt; ls -la /note.txt'
echo "print('hi from the sandbox')" | ate-env dev1 write /hi.py
ate-env dev1 read /hi.py
ate-env suspend dev1
kubectl ate get actors -a ate-env            # dev1 SUSPENDED
ate-env dev1 shell 'cat /note.txt'       # auto-resumes; verify the file is still there
```

> **Verify on your cluster:** env's `ActorTemplate` (from `manifest template` above) sets no explicit
> `durableDir` and no `snapshotsConfig.onPause`/`onCommit` scope — it relies on whatever your Substrate build
> defaults to for an unset scope. If the last command above comes back empty, that's the thing to check first
> (compare with module 3's explicit `SNAPSHOT_CONTENT_SCOPE_FULL`).

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
egressctl -a ate-env apply -f optional/H-env/egress-pypi.yaml --all
ate-env suspend dev1
ate-env dev1 shell '
  wget -q -T 5 -O /dev/null https://pypi.org/simple/ && echo "pypi: reachable" || echo "pypi: FAILED (check the policy)"
  wget -q -T 5 -O /dev/null https://example.com 2>/dev/null && echo "example.com: reachable (should not happen)" || echo "example.com: denied"
'
```

`wget` stands in for `curl` here for the reason in "Two things to know before you start." If your guest image
does have TLS trust roots baked in you'll see a clean 200-equivalent success for pypi.org; either way, the
important signal is the contrast between the two lines.

## 6. Find it on the dashboard

Same signals as Module 6, filtered to the `ate-env` atespace instead of `workshop`:

```bash
make prometheus-ui   # http://localhost:9090
```

Paste into the Prometheus UI (or Cloud Console → Monitoring → Metrics Explorer → PromQL):

| Question | PromQL |
|---|---|
| Fleet: worker state on env's pool | `max by ("ate.worker.state") ({"ate.workerpool.workers", "ate.workerpool.name"="default-template-workerpool"})` |
| Multiplexing: did ~10 envs ever need more than 2 workers? | `{"ate.workerpool.desired_workers", "ate.workerpool.name"="default-template-workerpool"}` vs `{"ate.workerpool.ready_workers", "ate.workerpool.name"="default-template-workerpool"}` |
| Suspend/resume latency for envN | `histogram_quantile(0.95, sum by (le, "ate.actor.operation.name") (rate({"ate.actor.lifecycle.operation.duration_bucket"}[5m])))` |
| Did `ate-env envN shell` trigger a resume or join a warm one? | `sum by ("ate.router.resume") (rate({"atenet.router.route.duration_count", top_level_controller_name="atenet-router"}[5m]))` |

In Cloud Console → Logging → Logs Explorer, the per-actor view from Module 6 works the same way, just with
the atespace label swapped:
```
resource.type="k8s_container"
resource.labels.namespace_name="ate-system"
"Actor state changed"
labels."ate.atespace"="ate-env"
```
That's the whole `dev1`/`env1`…`env10` fleet suspending and resuming, next to the fleet you already built.
The workshop's own Cloud Monitoring dashboard (`observability/dashboard.json`, applied in Module 0) and
substrate's three dashboards (applied by `make dashboards`) are fixed to the `workshop` atespace and the
`agents` WorkerPool, so they won't show `ate-env` traffic directly — use the ad hoc queries above for this
atespace, the same way you used the Prometheus UI in Module 6.

## Read the code

- `cmd/ate-env/manifest.go`: how env generates its namespace, WorkerPool, and API Deployment/Service, plus
  the separate `manifest template` subcommand that produces the ActorTemplate. Compare it with your own
  Module 2 manifests — notably, it sets no `workerSelector` on the template and no labels on the WorkerPool,
  relying on the default (unrestricted) match instead of the explicit `workload: workshop-agent` label pair
  you wrote by hand.
- `internal/ate/client.go`: a second production ateapi client — TLS, a re-read-on-every-call bearer token
  file, and the same `ate-target-actor` router header your gateway sets. Compare its `fileTokenCreds` with
  `app/internal/ateclient`'s `fileToken`: same idea, independently written.

## Clean up

```bash
for e in dev1 $(seq -f 'env%g' 1 10); do ate-env delete "$e"; done
ate-env manifest --api-image x --worker-image x | kubectl delete -f - --ignore-not-found
stop_port_forwards   # from scripts/lib.sh; stops step 2's ate-env-api forward (and any others this shell started)
```

## Next

Back to [Module 7 — Wrap-up](../../modules/07-wrap-up/README.md) for the rest of the optional labs.
