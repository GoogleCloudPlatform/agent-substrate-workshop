# Optional lab A — Agent environments with `agent-substrate/env` (30–45 min)

> **Cluster impact:** Namespace-scoped. Adds the `ate-env` namespace, atespace and WorkerPool next to the workshop; nothing in `workshop` changes. Step 5 (HTTPS egress rules) needs [lab I](../I-credential-injection/README.md)'s egress install, so skip it for now and come back after lab I.

[`agent-substrate/env`](https://github.com/agent-substrate/env) gives an LLM (or you) a **sandboxed shell
environment** that lives in a Substrate actor. You create it, run commands, read and write files, and let it
sleep. It wakes up with its files intact (verify this on your cluster — see the note in step 2). It exposes a
CLI, a Go client, a Python client, and a built-in **MCP** server.

It's the pattern you built in Modules 4–5 (an always-on API in front of suspendable actors), productized.

> **env is alpha and pre-v1.** No tagged release of env (latest: `v0.0.10`) pins a substrate build close to
> ours. This lab instead pins env at a specific commit on its `main` branch — see "Which env commit, and why"
> below for the compatibility check we ran before choosing it.

## Which env commit, and why

Our `SUBSTRATE_REF` is `v0.2.0` (pinned in `app/go.mod` as `github.com/agent-substrate/substrate v0.2.0`,
tagged 2026-09-25).

We checked every env tag and `main`:

| env ref | substrate pin (`go.mod`) | age vs. our pin |
|---|---|---|
| `v0.0.7`–`v0.0.10` | `v0.0.0-20260717234919-a2d55e99e02e` (2026-07-17) | ~2 months older |
| `main` @ `ab40c7b` | `v0.0.0-20260909202336-0b3d2d078f64` (2026-09-09) | 16 days older, before v0.2.0 |

No ref matches, and `main` hasn't moved since `ab40c7b` (checked 2026-09-27), so this lab pins:

```
ENV_REF=ab40c7bfb2049af1a7aade9e7bf9c6cac925b5ca   # env main, 2026-09-11
```

**You patch it to v0.2.0 before building.** v0.2.0 renamed two ActorTemplate fields env uses:
`containers[].readyz` is now `wakeupProbe`, and `snapshotsConfig` is now `snapshotConfig`. The field numbers
didn't change, so the gRPC wire format is the same. But `ate-env manifest template` prints YAML with the old
names, and v0.2.0's `kubectl ate create actor-template` rejects them. Step 1 bumps env's `go.mod` to v0.2.0
and renames the two fields in `cmd/ate-env/manifest.go` (plus its test). We ran that patch on a scratch
checkout: `go build`, `go vet`, and `go test ./...` all passed with no other changes. That's compile- and
unit-test-level evidence only. If your `SUBSTRATE_REF` has moved, or env's `main` has caught up to v0.2.0,
redo this check (or drop the patch) before trusting the pin.

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
ENV_REF=ab40c7bfb2049af1a7aade9e7bf9c6cac925b5ca
git clone https://github.com/agent-substrate/env .workshop/env 2>/dev/null; git -C .workshop/env checkout "$ENV_REF"
cd .workshop/env
# Bring env up to v0.2.0 (see "Which env commit, and why"). Safe to rerun.
go get github.com/agent-substrate/substrate@v0.2.0 && go mod tidy
sed -i.bak -e 's/ContainerReadyz/ContainerWakeupProbe/g; s/Readyz:/WakeupProbe:/g; s/\.Readyz/.WakeupProbe/g' \
  -e 's/SnapshotsConfig/SnapshotConfig/g' cmd/ate-env/manifest.go cmd/ate-env/manifest_test.go && rm cmd/ate-env/*.bak
go build ./... && go test ./cmd/ate-env/
export KO_DOCKER_REPO
API_IMAGE=$(ko build --base-import-paths ./cmd/ate-env-api)
GUEST_IMAGE=$(ko build --base-import-paths ./cmd/ate-env-guest)   # ko's "git is in a dirty state" warning is the patch above; ignore it
go build -o "$REPO_ROOT/bin/ate-env" ./cmd/ate-env
cd "$REPO_ROOT"

# --replicas 2 matches this workshop's WorkerPool size (module 2); env's own default is 5.
# env's WorkerPool carries no labels and its template no workerSelector, so env's actors would run on any
# worker, the workshop's `agents` included. The awk adds a `workload: ate-env` label to env's WorkerPool;
# the printf adds the template's workerSelector for it. Same pairing as your Module 2 manifests.
ate-env manifest --api-image "$API_IMAGE" --worker-image "$(cat .workshop/worker-image)" --replicas 2 \
  | awk '/^kind: WorkerPool$/ {wp=1} wp && /^metadata:$/ {print; print "  labels:"; print "    workload: ate-env"; wp=0; next} {print}' \
  | kubectl apply -f -
kubectl -n ate-env rollout status deploy/ate-env-api
kubectl ate create atespace ate-env   # the template below lives in it, and env only creates it on first use
{ ate-env manifest template --guest-image "$GUEST_IMAGE" --snapshots-bucket "gs://$BUCKET_NAME/ate-env/"
  printf 'workerSelector:\n  matchLabels:\n    workload: ate-env\n'; } \
  | kubectl ate create actor-template -f -
kubectl ate get atespaces                                      # "ate-env" is listed next to "workshop"
kubectl ate get actor-template default-template -a ate-env     # repeat until GOLDEN TAG is set (~20s)
```

This creates a Kubernetes namespace `ate-env` with its own WorkerPool (`default-template-workerpool`, 2
workers; it sets no CPU or memory limits, so `kubectl ate get workers -n ate-env` shows each worker offering
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

> **Verify on your cluster:** env's `ActorTemplate` (from `manifest template` above) sets no explicit
> `durableDir` and no `snapshotConfig.onPause`/`onCommit` scope — on Substrate `v0.2.0` both default to
> `SNAPSHOT_CONTENT_SCOPE_FULL`, so `/note.txt` survives the suspend. If the last command above comes back
> empty on a newer Substrate build, check its default scope first (compare with module 3's explicit
> `SNAPSHOT_CONTENT_SCOPE_FULL`).

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

## 5. Lock down egress (env sets no policy) — after lab I

> **Do this step after [lab I](../I-credential-injection/README.md).** A hostname rule for an **HTTPS** host
> only works when the egress gateway can read the TLS SNI, which takes lab I's `--experimental-use-sdsmint`
> install. On the workshop's default install, pypi.org stays unreachable even with this policy in place. Go on
> to step 6 now, and come back here once lab I is done.

From the workshop you know an actor with no EgressPolicy has no outbound network at all. Give the sandboxes
just PyPI, the way you'd lock down a real coding-agent sandbox:

```bash
source scripts/ate-api.sh
scripts/forward.sh ate-env-api ate-env svc/ate-env-api 7777 7777   # restart the forward if lab I's `make stop-forwards` stopped it
egressctl -a ate-env apply -f optional/A-env/egress-pypi.yaml --all
ate-env suspend dev1
ate-env dev1 shell '
  wget -q --no-check-certificate -T 5 -O /dev/null https://pypi.org/simple/ && echo "pypi: reachable" || echo "pypi: FAILED (check the policy)"
  wget -q --no-check-certificate -T 5 -O /dev/null https://example.com 2>/dev/null && echo "example.com: reachable (should not happen)" || echo "example.com: denied"
'
```

`wget` stands in for `curl` here for the reason in "Two things to know before you start." Why
`--no-check-certificate`? Under sdsmint the gateway terminates TLS and presents a certificate it minted for
pypi.org, which chains to the gateway's own CA. The env template doesn't mount that CA (lab I's step 5 shows
how, with a `systemInfo` trust-bundle volume), so a verifying client stops at `certificate verify failed`.
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

- `cmd/ate-env/manifest.go`: how env generates its namespace, WorkerPool, and API Deployment/Service, plus
  the separate `manifest template` subcommand that produces the ActorTemplate. Compare it with your own
  Module 2 manifests — notably, it sets no `workerSelector` on the template and no labels on the WorkerPool,
  relying on the default (unrestricted) match instead of the explicit `workload: workshop-agent` label pair
  you wrote by hand. That's why step 1 adds a `workload: ate-env` pair: without it, env's actors run on the
  workshop's `agents` workers, env's own pool sits idle, and step 6's pool queries show nothing.
- `internal/ate/client.go`: a second production ateapi client — TLS, a re-read-on-every-call bearer token
  file, and the same `ate-target-actor` router header your gateway sets. Compare its `fileTokenCreds` with
  `app/internal/ateclient`'s `fileToken`: same idea, independently written.

## Clean up

If you plan to run [step 5](#5-lock-down-egress-env-sets-no-policy--after-lab-i) after [lab I](../I-credential-injection/README.md), wait to run this clean-up until after step 5:

```bash
for e in dev1 $(seq -f 'env%g' 1 10); do ate-env delete "$e"; done
kubectl ate delete actor-template default-template -a ate-env
kubectl ate delete atespace ate-env
ate-env manifest --api-image x --worker-image x | kubectl delete -f - --ignore-not-found
make stop-forwards   # stops step 2's ate-env-api forward (and the workshop's other port-forwards)
```

## Next

Back to [Module 7 — Wrap-up](../../modules/07-wrap-up/README.md) for the rest of the optional labs.
