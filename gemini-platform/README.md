# Enterprise AI Agents with Agent Substrate and Gemini Agent Platform on GKE

Build, upgrade, govern, and evaluate a stateful **ADK Go v2** (`google.golang.org/adk/v2`) **Platform SRE Triage Agent** running as an **Incident Actor** inside **Agent Substrate (`v1.0.0`)** on Google Kubernetes Engine (GKE), integrated across the **Actor Boundary** with **Gemini Enterprise Agent Platform**.

> Enterprise AI Agents workshop of [`GoogleCloudPlatform/agent-substrate-workshop`](https://github.com/GoogleCloudPlatform/agent-substrate-workshop).

In ~90 minutes across 8 modules, you:

- Validate uniform-CPU GKE `1.37+` prerequisites, apply a quota-safe `ComputeClass` (`substrate-gvisor`), install Agent Substrate `v1.0.0`, and boot a live degraded microservice (`ledger-api`) alongside a self-contained Git, Ticket, MCP, and OAuth 2.0 **Operations Hub** (`ops-hub`) in `prod-sim`.
- Wire a keyless **ADK Go v2** triage agent inside a `gVisor` **Incident Actor** (`port 80`, `Golden Snapshot`, `/workspace` durable directory) routed across strict `EgressPolicy` through an always-on **Governed Broker** (`sre-gateway`) serving a **Google Cloud Console**-modeled **Incident Console** over **Gemini on Agent Platform** (Model Garden).
- Persist turn events to a **Session Ledger** (**Gemini Sessions**) for zero-wake (`0 vCPU`) inspection and extract **Consolidated Memory** (**Gemini Memory Bank**) that survives container template upgrades (`v1` -> `v2`) and recalls across sibling Incident Actors.
- Package and pull SHA-256-verified **Procedural Skills** from **Skill Registry** (`v1` -> `v2`) at runtime with zero container rebuilds and zero Golden Snapshot churn.
- Register an **A2A** Agent Card and discover governed **MCP** servers from **Agent Registry**, pausing at an ADK human-in-the-loop **Remediation Gate** (`toolconfirmation`) that suspends the Actor to Cloud Storage (`0 vCPU`) and wakes in under 400 ms upon operator approval to push the verified git fix.
- Enforce defense-in-depth zero-trust governance in Module 05 with declarative GKE **Agent Gateway** (`Gateway` + `GCPAgentGateway` + `GCPAuthzPolicy`), Pod **Agent Identity** SPIFFE credentials, **Auth Manager** 3-legged OAuth (`adk/v2/auth/gcp`), live **Model Armor** prompt-injection blocking on `INC-409`, MCP `readOnlyHint`/`destructiveHint` policy enforcement, and `0`-token snapshot audits.
- Export secret-scrubbed W3C **OpenTelemetry** GenAI spans to **Cloud Trace** and score live sessions with a pure-Go **Gemini on Agent Platform** autorater (`make eval`).

## Agenda

| # | Section | Min | What you build & learn |
|---|---|---|---|
| 0 | [Setup](modules/00-setup/README.md) | 10 | Uniform-CPU preflight, quota-safe `ComputeClass`, Substrate `v1.0.0`, async gateway/runtime bootstrap, live `prod-sim` (`ledger-api` + `ops-hub`) |
| 1 | [Actor boundary](modules/01-actor-boundary/README.md) | 12 | Keyless ADK Go v2 **Incident Actor** (`v1`), **Governed Broker** (`sre-gateway`), Cloud Console **Incident Console**, Turn 1 repo clone & triage |
| 2 | [Sessions & memory](modules/02-sessions-and-memory/README.md) | 15 | **Gemini Sessions** zero-wake `ListEvents` (`0 vCPU`) and **Gemini Memory Bank** cross-rollout (`v1` -> `v2`) & sibling-actor recall |
| 3 | [Skill registry](modules/03-skill-registry/README.md) | 12 | **Skill Registry** SHA-256 bundle (`SKILL.md` + diagnostic script) and zero-rebuild `v1` -> `v2` rollout via `SkillToolset` |
| 4 | [Agent registry, MCP & A2A](modules/04-agent-registry-mcp-a2a/README.md) | 14 | **Agent Registry** A2A Agent Card + MCP discovery and `toolconfirmation` **Remediation Gate** (`0 vCPU` suspend -> under 400 ms resume) |
| 5 | [Zero-trust governance](modules/05-zero-trust-governance/README.md) | 14 | GKE `GCPAgentGateway` + `GCPAuthzPolicy`, Pod **Agent Identity**, **Auth Manager** 3LO, live **Model Armor** block (`INC-409`), MCP `403`, `0`-token snapshot audit |
| 6 | [Observability & evals](modules/06-observability-and-evals/README.md) | 10 | Scrubbed W3C OpenTelemetry `gen_ai.*` traces in Cloud Trace, pure-Go `make eval` autorater, SCC Anomaly Detection |
| 7 | [Wrap-up](modules/07-wrap-up/README.md) | 3 | Production architecture patterns, optional Ambient Mesh + Secure Web Proxy egress overlay, and full teardown |

## Prerequisites & classroom sizing

- **Cluster footprint**: Total workshop pod requests (`2× 250m / 1Gi` `WorkerPool`, `100m / 128Mi` `sre-gateway`, `50m / 64Mi` `ledger-api`, and `50m / 64Mi` `ops-hub` = `700m` CPU / `2.25Gi` RAM) schedule directly onto either a bring-your-own `2× c3-standard-4` cluster or a pre-provisioned Google Skills `2× n1-standard-4` (`Intel Skylake`) classroom cluster without requiring extra nodes.
- **Local tools**: `gcloud`, `kubectl`, `go` 1.26+ (with `GOTOOLCHAIN=auto` fetching `go1.27` from `app/go.mod`, or `go` 1.27+), `ko`, `jq`, `envsubst`, `curl`, `git`, `python3`.
## Quick start

```bash
git clone https://github.com/GoogleCloudPlatform/agent-substrate-workshop
cd agent-substrate-workshop/gemini-platform
cp workshop.env.example workshop.env   # set PROJECT_ID and CLUSTER_LOCATION
make preflight
make gcp
export PATH="$PWD/bin:$PATH"
source scripts/env.sh
make tools
make install-bg
make prod-sim
make install-status                    # wait until "install finished"
make verify MODULE=0
```

## Repository layout

| Path | Description |
|---|---|
| `modules/NN-*/` | Step-by-step module guides, Kubernetes/Substrate manifest templates, and progressive `solution/` files |
| `modules/img/` | Cloud Console-styled Apache-2.0 SVG architecture diagrams (`01` through `06`) |
| `codelabs/` | Consolidated DevSite Codelabs edition (`en.md` + `img/`, including optional overlay appendix) |
| `qwiklabs/` | Consolidated Google Skills / Qwiklabs classroom edition (`en.md` + `img/`, core modules only) |
| `instructor/` | Event delivery `runbook.md` (benchmarks, 60m/90m cut lines, HotLab sizing) and `troubleshooting.md` |
| `app/` | Go `1.27` module: `cmd/ledger-api`, `cmd/ops-hub`, `cmd/ledger-verify`, `cmd/agent`, `cmd/sre-gateway`, and `cmd/evalrunner` |
| `scripts/` | Automation, rendering, SPIFFE auto-detection, per-module `verify/`, `catch-up.sh`, and offline `scripts/test/` suite |
| `docs/` | Architecture Decision Records (`docs/adr/0001..0004`) and agent workflow conventions (`docs/agents/`) |
| `DESIGN.md` | Google Cloud Console Design System specification for the Incident Console |
| `GLOSSARY.md` | Ubiquitous domain language across code, UI, manifests, and curriculum prose |

## Offline tests

Run the complete offline Go and shell/hygiene contract suite (no cluster or cloud credentials required):

```bash
make test
```

## Clean up

Run `make teardown YES=1` to remove `ATESPACE` (`workshop-301`), namespaces `workshop-301` and `prod-sim`, `ComputeClass/substrate-gvisor`, and Cloud Storage actor snapshots while preserving your cluster (omit `YES=1` for an interactive confirmation prompt). Pass `ALL=1` (`make teardown ALL=1 YES=1`) to also delete the snapshot bucket, Artifact Registry repository, managed session/memory runtime, IAM bindings, and snapshot transfer service account. Run `make delete-cluster YES=1` to release all PersistentVolumeClaims before deleting the GKE cluster.

## License and disclaimers

This project is licensed under the Apache License, Version 2.0: see [LICENSE](LICENSE) for details and [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidelines.

This project is intended for demonstration and educational workshop purposes only. It is not intended for use in a production environment.

Use of Google Cloud services in this workshop is subject to the [Google Cloud Terms of Service](https://cloud.google.com/terms) and the [Google Cloud Service Specific Terms](https://cloud.google.com/terms/service-terms).

Eligibility for the [Google Open Source Software Vulnerability Rewards Program](https://bughunters.google.com/open-source-security) is determined by the [Google Open Source Software Vulnerability Reward Program Rules](https://bughunters.google.com/about/rules/open-source/google-open-source-software-vulnerability-reward-program-rules).
