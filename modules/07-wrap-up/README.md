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
- **Idle is your job** (Module 5), and so is WorkerPool sizing (optional lab G).
- IPv4 only, no GPUs, and gVisor supports one `durableDir` per actor.
- **No actor-emitted metrics yet.** Actors share hostname and IP inside the sandbox, so `/metrics` has
  to live on the always-on tier (Module 6). Per-actor detail lives in logs instead.
- Template env is literal and snapshotted. Keep secrets in a gateway or use egress credential injection (optional lab C).
- Substrate moves fast. Pin `SUBSTRATE_REF` and rebuild worker images with the control plane.

## Keep going: optional labs

| Lab | What you'll do |
|---|---|
| **[H — Agent environments with `agent-substrate/env`](../../optional/H-env/README.md)** | Install `ate-env` on your cluster and drive sandboxed shells through a CLI and MCP. It's your gateway, productized. |
| [D — Observability deep dive](../../optional/D-observability-deep-dive/README.md) | Alerts, SLOs, tracing, a per-actor drill-down dashboard |
| [A — Tags, clone, revert](../../optional/A-tags-clone-revert/README.md) | Snapshot an actor, clone it, roll it back |
| [E — agent-swarm at 50:20](../../optional/E-agent-swarm/README.md) | Deploy the reference multi-agent demo |
| [B — microVM sandbox class](../../optional/B-microvm/README.md) | Stronger isolation than gVisor |
| [C — Egress credential injection](../../optional/C-credential-injection/README.md) | `ate-secret://`: let egress add credentials so actors never hold them |
| [F — Your own gRPC client](../../optional/F-grpc-client/README.md) | Drive ateapi directly from Go |
| [G — Autoscale the WorkerPool](../../optional/G-hpa-workerpool/README.md) | HPA on worker demand |
| [I — What counts as idle?](../../optional/I-what-counts-as-idle/README.md) | Background work vs idle-suspend: a heartbeat that keeps a busy actor awake |

## Clean up
```bash
make teardown          # workshop resources; keeps the cluster, the bucket, and Artifact Registry
YES=1 scripts/teardown.sh --all   # also deletes the bucket and the Artifact Registry repo
```
