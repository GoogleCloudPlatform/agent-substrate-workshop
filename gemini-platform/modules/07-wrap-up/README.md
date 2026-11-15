# Wrap-up and production patterns (3 min)

You have built, upgraded, governed, traced, and evaluated a stateful **ADK Go v2** **Platform SRE Triage Agent** running inside **Agent Substrate (`v1.0.0`)** on GKE and integrated across the **Actor Boundary** with **Gemini Enterprise Agent Platform**.

## Architectural synthesis: inside vs. outside the Actor Boundary

| Layer | Inside the Actor Boundary (Agent Substrate on GKE) | Across the Actor Boundary (Governed Broker & Gemini Enterprise Agent Platform) |
|---|---|---|
| **Compute & state** | Per-incident `gVisor` **Incident Actor** (`inc-402`, `inc-403`) booted in `< 400ms` from a shared **Golden Snapshot** with a durable `/workspace` git checkout; suspends to Cloud Storage at **`0 vCPU` / `0 MB RAM`** when idle or awaiting human approval. | **Session Ledger** (**Gemini Sessions**) serves zero-wake `ListEvents` while Actors sleep; **Consolidated Memory** (**Gemini Memory Bank**) distills root causes across `v1 -> v4` rollouts and sibling incidents. |
| **Capabilities & rollout** | `ADK Go v2` `SkillToolset` unpacks verified archives into `/workspace/.skills/`; `toolconfirmation` pauses high-blast-radius remediations cleanly. | **Skill Registry** ships SHA-256-verified `SKILL.md` + POSIX scripts (`v1 -> v2`) with zero container rebuilds; **Agent Registry** advertises `/.well-known/agent-card.json` (**A2A**) and governed **MCP** servers. |
| **Identity & credentials** | **100% keyless sandbox**: zero Workload Identity tokens, zero OAuth client secrets, and zero bearer tokens in env vars, `/workspace`, or Cloud Storage memory snapshots (`make audit-snapshot`). | `sre-gateway` holds **Pod Agent Identity** SPIFFE SVIDs + **Auth Manager** 3LO OAuth (`S256` PKCE) in an ephemeral `<= 15m` in-memory vault, injecting credentials only on outbound hops with dual `operator + agent` attribution. |
| **Defense & observability** | Default-deny `EgressPolicy` restricts sandbox egress strictly to `sre-gateway`; ADK emits `invoke_agent` & `execute_tool` spans over keyless `/otlp/v1/traces`. | `GCPAgentGateway` + `GCPAuthzPolicy` + **Model Armor** block prompt injections (`INC-409`) inline; `sre-gateway` scrubs secrets before **Cloud Trace** export, runs 4-rubric pure-Go **Online Evals**, and feeds **SCC Anomaly Detection**. |

## Optional production pattern: GKE Ambient Networking + Secure Web Proxy (`AGENT_TO_ANYWHERE`)

Inside `ATESPACE`, Agent Substrate's `atenet-egress` enforces L7 hostname `EgressPolicy` so **Incident Actors** can talk only to `sre-gateway`. In production environments where `sre-gateway` itself connects to external SaaS APIs (such as GitHub.com, PagerDuty, or Slack), platform teams layer **GKE Ambient Networking** (`GCPEgressPolicy` + `GCPBackend`) with **Secure Web Proxy (`AGENT_TO_ANYWHERE`)** on the broker namespace so every external TLS connection from `sre-gateway` traverses a centrally audited L7 proxy without sidecars.

Inspect the rendered production overlay in [`manifests/ambient-swp-egress-overlay.yaml.tmpl`](manifests/ambient-swp-egress-overlay.yaml.tmpl):

```bash
source scripts/env.sh
scripts/render.sh modules/07-wrap-up/manifests/ambient-swp-egress-overlay.yaml.tmpl
```

Note the two GKE `networking.gke.io/v1` schema rules enforced by the manifest:
1. **`GCPBackend` (`sre-swp-egress-backend`)** specifies **only** `spec.serviceUris` containing the single **Secure Web Proxy** gateway resource URI (`projects/${PROJECT_ID}/locations/${REGION}/gateways/sre-swp-egress-gateway`).
2. **`GCPEgressPolicy` (`name: default`, singleton per namespace)** targets `GCPBackend/sre-swp-egress-backend` and sets `excludeCIDRRanges` (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`) so East-West cluster traffic (`atenet-router`, `ops-hub`, `ledger-api`) stays direct on the cluster network while internet-bound egress routes through Secure Web Proxy URL lists.

## Production checklist for compute obtainability & snapshot fidelity

- **Uniform CPU microarchitecture per `WorkerPool`**: Memory-snapshot restore (`SNAPSHOT_FIDELITY_MEMORY`) requires matching host CPU instruction sets across suspend and resume nodes. Pin production `WorkerPool` pods to a `ComputeClass` (`c3`, `n4`, or `n2`) with explicit `nodePoolAutoCreation` rules and `consolidationDelayMinutes` tuned above your active turn window.
- **Zero-token snapshot invariant in CI/CD**: Run `scripts/audit-snapshot.sh` and `bin/evalrunner --min-score 0.85` as release gates whenever changing broker routes or MCP tool annotations.

## Checkpoint

Verify Module 07 manifests and teardown readiness before cleaning up your environment:

```bash
make verify MODULE=7
```

## Clean up

Choose the cleanup level matching how you provisioned your environment:

1. **Keep the GKE cluster & `ate-system`, remove workshop workloads (`workshop-301`, `prod-sim`, `ComputeClass`, snapshots)**:
   ```bash
   make teardown YES=1
   ```
2. **Full project resource cleanup (also removes the snapshot GCS bucket, Artifact Registry repo, managed session/memory runtime, Model Armor template, AuthProvider, Skill Registry bundle, and IAM bindings)**:
   ```bash
   make teardown ALL=1 YES=1
   ```
3. **Delete a self-created GKE cluster cleanly (releases PersistentVolumes first)**:
   ```bash
   make delete-cluster YES=1
   ```
