# Wrap-up (3 min)

The goal of this section is to review how the primitives you used map to real-world agent systems, cover the platform's current limits, and show how to clean up or continue with the optional labs.

## What you built

| What you built | Substrate primitive | Where production systems use it |
|---|---|---|
| `WorkerPool` `agents` | Generic, pre-started capacity | Every deployment |
| Templates `agent-v1` → `agent-v3` | Immutable template and golden snapshot | **google/ax (`Task`)**: hashes image and env into `<task>-tmpl-<hash>` |
| `hello`, `memo` | Suspend/resume, `MEMORY` vs. `VOLUMES` fidelity | **google/ax (`Workspace`)**: `/workspace` `durableDir` with `VOLUMES` fidelity |
| Gateway `/v1/model` | Presence vs. cognition split | **always-on-agent**: presence service and suspendable cognition actor |
| Gateway-only `EgressPolicy` | Deny-by-default egress | **agent-substrate/env**: sandboxed shells for LLM tools |
| `swarm-01…20` and idle-suspend | Density and request parking | **agent-swarm**: 50 agents on 2–5 autoscaled workers, 4 per worker |
| Dashboards and logs | OpenTelemetry and Managed Service for Prometheus | Operating any of the systems above |
| `buddy` remembers your name | Durable per-actor state | **hermes**: an agent with durable identity |

**Going one layer up: [`agent-substrate/env`](https://github.com/agent-substrate/env) and [`google/ax`](https://github.com/google/ax).**
From "Hello actor" through "Density and idle", you configured templates, `/data` persistence, readiness probes, routing headers, and model access
by hand to see how each Substrate primitive works. Higher-level tools package those patterns for you:
`agent-substrate/env` ([optional lab A](../../optional/A-env/README.md)) wraps a sandboxed shell and file API in an
actor with a built-in MCP server, and **AX** ([optional lab B](../../optional/B-ax/README.md)) embeds `env`'s guest
gRPC services (`ax ssh`) inside a declarative `Task`, `Workspace`, and `Model` control plane (`ax apply -f`).

For comparison, **agent-sandbox** (`kubernetes-sigs/agent-sandbox`) addresses a similar use case — running a
Pod per agent session — with no Substrate integration. Its `SandboxTemplate` corresponds to
`ActorTemplate` and `SandboxWarmPool` corresponds to `WorkerPool`, and both projects can suspend and resume. The difference is that many actors share a few generic workers,
and a suspended actor resumes from its memory and disk snapshot on any of them. See the "Architecture tour" section for the full comparison table.

## Limits to know before you build
- **Authorization inside an atespace is role-based (`AccessPolicy`).** Any principal granted `editor` or `admin` on an atespace can act on all actors in that atespace (there are no per-actor ACLs).
- **Without `--token-file`, `kubectl ate` acts as the shared `ate-client` identity**, which the installer makes a global owner.
  Substrate's authorization guide says not to keep that setup outside development. On a shared install, pass `--token-file` so
  that each person acts as themselves.
- **`CRASHED` actors require intervention** (from a human or your controller): `resume` rejects crashed actors, so run `kubectl ate revert actor X -a Y`
  to roll the actor back to `SUSPENDED` at its last snapshot before resuming, or delete it with `kubectl ate delete actor X -a Y`.
- **Idle detection is your application's responsibility** (see "Density and idle"), and so is `WorkerPool` sizing (see optional lab G).
- Substrate currently supports IPv4 only, no GPUs, and one `durableDir` per actor under gVisor.
- **Managed Prometheus doesn't scrape actors directly.** Because actors share a generic hostname and IP address inside the sandbox, Prometheus
  scraping runs on the always-on tier (see "Observe the fleet"), or actors push OTLP metrics to the collector through an `http`
  `EgressPolicy` rule (`docs/observability.md`). Per-actor details live in logs.
- Template environment variables are literal and stored in snapshots. Keep secrets in a gateway or use egress credential injection (see optional lab J).
- Substrate evolves quickly. Pin `SUBSTRATE_REF` and rebuild worker images alongside the control plane.

## Keep going: optional labs

The optional labs appear in order of how much they modify your cluster. Complete them in order so that a later lab
doesn't affect an earlier one:
- **Labs A–E** stay within their own namespace and actors.
- **Labs F–H** and **J** modify shared, cluster-wide resources; run each lab's cleanup step before moving on.
- **Lab I** adds a nested-virtualization node pool and a cluster-wide `SandboxConfig`.

| Lab | What you do | Cluster impact |
|---|---|---|
| **[A — Agent environments with `agent-substrate/env`](../../optional/A-env/README.md)** (start here) | Install `ate-env` on your cluster and drive sandboxed shells through a CLI and MCP. | Adds the `ate-env` namespace, atespace, and `WorkerPool` |
| **[B — Declarative tasks and workspaces with `google/ax`](../../optional/B-ax/README.md)** | Deploy the AX control plane (`ax-server`), apply `Workspace` and `Task` manifests, inspect the Substrate records it creates, and open a shell with `ax ssh`. | Adds the `ax-system` namespace, `ax-workshop` atespace, and `ax-workers` `WorkerPool` |
| [C — What counts as idle?](../../optional/C-what-counts-as-idle/README.md) | Coordinate background work with idle-suspend using a heartbeat that keeps a busy actor awake. | Uses your agent and adds one new template |
| [D — Tags, clone, revert](../../optional/D-tags-clone-revert/README.md) | Tag an actor snapshot, clone it into a new actor, and revert state. | Uses your actors; toggles idle-suspend off and back on |
| [E — Your own gRPC client](../../optional/E-grpc-client/README.md) | Call `ateapi` directly from Go. | Uses your actors |
| [F — Observability deep dive](../../optional/F-observability-deep-dive/README.md) | Configure alerts, SLOs, tracing, and a per-actor drill-down dashboard. | Creates project alert policies; deliberately crashes actors on two workers and saturates the pool |
| [G — Autoscale the WorkerPool](../../optional/G-hpa-workerpool/README.md) | Configure a HorizontalPodAutoscaler based on worker demand. | Claims the cluster's `external.metrics.k8s.io` API; clean up before lab H |
| [H — agent-swarm at 50:20](../../optional/H-agent-swarm/README.md) | Deploy the reference multi-agent demo. | Claims the `external.metrics.k8s.io` API, patches router flags, and requests ~11.5 GiB of memory; clean up afterward |
| [I — microVM sandbox class](../../optional/I-microvm/README.md) | Run actors with hardware-virtualized microVM isolation instead of gVisor. | Adds a tainted nested-virtualization node pool and a cluster-wide `SandboxConfig`; incurs additional node cost |
| [J — Egress credential injection](../../optional/J-credential-injection/README.md) | Use `ate-secret://` rules so the egress proxy injects credentials without exposing them to actors. | Adds a `workshop-secrets` namespace and grants `workshop` access in `k8s-credential-provider-namespace-policy` |

## Clean up
If the project exists only for this workshop, deleting the project is the simplest complete cleanup. Otherwise:
```bash
make teardown          # deletes workshop resources; keeps the cluster, bucket, and Artifact Registry repository
YES=1 scripts/teardown.sh --all   # also deletes the bucket, Artifact Registry repository, IAM bindings, and snapshot transfer service account
make delete-cluster    # deletes the cluster's PersistentVolumeClaims (and their disks) first, then deletes the cluster
```
At an instructor-led event, check with your instructor before you delete a cluster that they provisioned.
