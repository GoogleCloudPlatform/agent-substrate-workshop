# Module 7 — Wrap-up (3 min)

## What you built

| You did | Substrate primitive | Where real systems use it |
|---|---|---|
| WorkerPool `agents` | generic, pre-started capacity | every deployment |
| Templates v1 → v3 | immutable template + golden snapshot | fast clone-from-snapshot starts |
| `hello`, `memo` | suspend/resume, FULL vs DATA scope | agents that sleep between turns |
| gateway `/v1/model` | presence vs cognition split | **always-on-agent**: presence service + suspendable cognition actor |
| gateway-only EgressPolicy | deny-by-default egress | **agent-substrate/env**: sandboxed shells for LLM tools |
| `swarm-01…20` + idle-suspend | density + parking | **agent-swarm**: 50 agents on 2–5 autoscaled workers, 4 per worker |
| dashboards + logs | OTel + GMP | operating any of the above |
| `buddy` remembers you | durable per-actor state | **hermes**: an agent with durable identity |

For comparison, **agent-sandbox** (`kubernetes-sigs/agent-sandbox`) solves a similar problem — a
Pod per agent session — with no Substrate integration and none planned. Its `SandboxTemplate` ≈
`ActorTemplate`, `SandboxWarmPool` ≈ `WorkerPool`, but a Sandbox suspend deletes the Pod, where an
actor suspend checkpoints memory onto any worker. They're not related projects; the comparison is
just useful (Module 1 has the full table).

## Limits to know before you build
- **No authz inside an atespace yet.** Anyone who can call ateapi for an atespace can act on all of its actors.
- **CRASHED** actors need a human (or your code): resume refuses them, so `kubectl ate revert actor X -a Y`
  (back to `SUSPENDED` with its last snapshot), then resume, or `kubectl ate delete actor X -a Y`.
- **Idle is your job** (Module 5), and so is WorkerPool sizing (optional lab F).
- IPv4 only, no GPUs, and gVisor supports one `durableDir` per actor.
- **No actor-emitted metrics yet.** Actors share hostname and IP inside the sandbox, so `/metrics` has
  to live on the always-on tier (Module 6). Per-actor detail lives in logs instead.
- Template env is literal and snapshotted. Keep secrets in a gateway or use egress credential injection (optional lab I).
- Substrate moves fast. Pin `SUBSTRATE_REF` and rebuild worker images with the control plane.

## Keep going: optional labs

The labs are ordered by how much they change your cluster, so do them in order and nothing a later lab
does can break an earlier one. **A–D** stay inside your own namespace and actors. **E–G** touch shared,
cluster-wide pieces; finish each one's clean-up before moving on. **H–I** change the cluster itself, so do them
last. Each lab's README opens with the same impact note.

| Lab | What you'll do | Cluster impact |
|---|---|---|
| **[A — Agent environments with `agent-substrate/env`](../../optional/A-env/README.md)** (start here) | Install `ate-env` on your cluster and drive sandboxed shells through a CLI and MCP. It's your gateway, productized. | Adds the `ate-env` namespace, atespace and WorkerPool. Step 5 needs lab I |
| [B — What counts as idle?](../../optional/B-what-counts-as-idle/README.md) | Background work vs idle-suspend: a heartbeat that keeps a busy actor awake | Your agent and one new template |
| [C — Tags, clone, revert](../../optional/C-tags-clone-revert/README.md) | Snapshot an actor, clone it, roll it back | Your actors; switches idle-suspend off and back on |
| [D — Your own gRPC client](../../optional/D-grpc-client/README.md) | Drive ateapi directly from Go | Your actors |
| [E — Observability deep dive](../../optional/E-observability-deep-dive/README.md) | Alerts, SLOs, tracing, a per-actor drill-down dashboard | Project alert policies; deliberately crashes the actors on two workers and saturates the pool |
| [F — Autoscale the WorkerPool](../../optional/F-hpa-workerpool/README.md) | HPA on worker demand | Claims the cluster's external-metrics API. Clean up before G |
| [G — agent-swarm at 50:20](../../optional/G-agent-swarm/README.md) | Deploy the reference multi-agent demo | Claims the external-metrics API, patches router flags (not restored), needs ~11.5 GiB. Clean up after |
| [H — microVM sandbox class](../../optional/H-microvm/README.md) | Stronger isolation than gVisor | Adds a tainted nested-virt node pool and a cluster-wide SandboxConfig; extra cost |
| [I — Egress credential injection](../../optional/I-credential-injection/README.md) | `ate-secret://`: let egress add credentials so actors never hold them | **Reinstalls substrate** with the experimental egress dataplane. Do it last, or on a fresh cluster |

## Clean up
```bash
make teardown          # workshop resources; keeps the cluster, the bucket, and Artifact Registry
YES=1 scripts/teardown.sh --all   # also deletes the bucket and the Artifact Registry repo
```
