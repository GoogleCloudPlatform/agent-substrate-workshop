# Module 7 — Wrap-up (3 min)

## What you built

| You did | Substrate primitive | Where real systems use it |
|---|---|---|
| WorkerPool `agents` | generic, pre-started capacity | every deployment |
| Templates v1 → v3 | immutable template + golden snapshot | **google/ax (`Task`)**: hashes image + env into `<task>-tmpl-<hash>` |
| `hello`, `memo` | suspend/resume, MEMORY vs VOLUMES fidelity | **google/ax (`Workspace`)**: `/workspace` `durableDir` with `VOLUMES` fidelity |
| gateway `/v1/model` | presence vs cognition split | **always-on-agent**: presence service + suspendable cognition actor |
| gateway-only EgressPolicy | deny-by-default egress | **agent-substrate/env**: sandboxed shells for LLM tools |
| `swarm-01…20` + idle-suspend | density + parking | **agent-swarm**: 50 agents on 2–5 autoscaled workers, 4 per worker |
| dashboards + logs | OTel + GMP | operating any of the above |
| `buddy` remembers you | durable per-actor state | **hermes**: an agent with durable identity |

**Going one layer up: [`agent-substrate/env`](https://github.com/agent-substrate/env) and [`google/ax`](https://github.com/google/ax).**
In Modules 2–5 you wired templates, `/data` persistence, readiness probes, routing headers, and model configuration
by hand so you could see how each Substrate primitive works. Higher-level tools package those patterns for you:
`agent-substrate/env` ([optional lab A](../../optional/A-env/README.md)) wraps a sandboxed shell and file API in an
actor with a built-in MCP server, and **AX** ([optional lab B](../../optional/B-ax/README.md)) embeds `env`'s guest
gRPC services (`ax ssh`) inside a declarative `Task` + `Workspace` + `Model` control plane (`ax apply -f`).

For comparison, **agent-sandbox** (`kubernetes-sigs/agent-sandbox`) solves a similar problem — a
Pod per agent session — with no Substrate integration and none planned. Its `SandboxTemplate` ≈
`ActorTemplate`, `SandboxWarmPool` ≈ `WorkerPool`, but a Sandbox suspend deletes the Pod, where an
actor suspend checkpoints memory onto any worker. They're not related projects; the comparison is
just useful (Module 1 has the full table).

## Limits to know before you build
- **Authz inside an atespace is role-based (`AccessPolicy`).** Anyone granted `editor` or `admin` on an atespace can act on all of its actors (not per-actor ACLs).
- **CRASHED** actors need a human (or your code): resume refuses them, so `kubectl ate revert actor X -a Y`
  (back to `SUSPENDED` with its last snapshot), then resume, or `kubectl ate delete actor X -a Y`.
- **Idle is your job** (Module 5), and so is WorkerPool sizing (optional lab G).
- IPv4 only, no GPUs, and gVisor supports one `durableDir` per actor.
- **Actors aren't scraped for `/metrics`.** Actors share hostname and IP inside the sandbox, so Prometheus
  scraping lives on the always-on tier (Module 6), or actors push OTLP to the collector through an `http`
  EgressPolicy rule (`docs/observability.md`). Per-actor detail lives in logs instead.
- Template env is literal and snapshotted. Keep secrets in a gateway or use egress credential injection (optional lab J).
- Substrate moves fast. Pin `SUBSTRATE_REF` and rebuild worker images with the control plane.

## Keep going: optional labs

The labs are ordered by how much they change your cluster, so do them in order and nothing a later lab
does can break an earlier one. **A–E** stay inside their own namespace and actors. **F–H** and **J** touch shared,
cluster-wide pieces; finish each one's clean-up before moving on. **I** adds a nested-virt node pool and
cluster-wide `SandboxConfig`. Each lab's README opens with the same impact note.

| Lab | What you'll do | Cluster impact |
|---|---|---|
| **[A — Agent environments with `agent-substrate/env`](../../optional/A-env/README.md)** (start here) | Install `ate-env` on your cluster and drive sandboxed shells through a CLI and MCP. It's your gateway, productized. | Adds the `ate-env` namespace, atespace and WorkerPool |
| **[B — Declarative tasks and workspaces with `google/ax`](../../optional/B-ax/README.md)** | Deploy the AX control plane (`ax-server`), apply `Workspace` and `Task` manifests, inspect the Substrate records it creates, and shell in with `ax ssh`. | Adds the `ax-system` namespace, `ax-workshop` atespace and `ax-workers` WorkerPool |
| [C — What counts as idle?](../../optional/C-what-counts-as-idle/README.md) | Background work vs idle-suspend: a heartbeat that keeps a busy actor awake | Your agent and one new template |
| [D — Tags, clone, revert](../../optional/D-tags-clone-revert/README.md) | Snapshot an actor, clone it, roll it back | Your actors; switches idle-suspend off and back on |
| [E — Your own gRPC client](../../optional/E-grpc-client/README.md) | Drive ateapi directly from Go | Your actors |
| [F — Observability deep dive](../../optional/F-observability-deep-dive/README.md) | Alerts, SLOs, tracing, a per-actor drill-down dashboard | Project alert policies; deliberately crashes the actors on two workers and saturates the pool |
| [G — Autoscale the WorkerPool](../../optional/G-hpa-workerpool/README.md) | HPA on worker demand | Claims the cluster's external-metrics API. Clean up before H |
| [H — agent-swarm at 50:20](../../optional/H-agent-swarm/README.md) | Deploy the reference multi-agent demo | Claims the external-metrics API, patches router flags (not restored), needs ~11.5 GiB. Clean up after |
| [I — microVM sandbox class](../../optional/I-microvm/README.md) | Stronger isolation than gVisor | Adds a tainted nested-virt node pool and a cluster-wide SandboxConfig; extra cost |
| [J — Egress credential injection](../../optional/J-credential-injection/README.md) | `ate-secret://`: let egress add credentials so actors never hold them | Adds a `workshop-secrets` namespace and grants `workshop` access in `k8s-credential-provider-namespace-policy` |

## Clean up
```bash
make teardown          # workshop resources; keeps the cluster, the bucket, and Artifact Registry
YES=1 scripts/teardown.sh --all   # also deletes the bucket and the Artifact Registry repo
```
