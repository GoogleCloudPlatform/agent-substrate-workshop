# Agent Substrate Workshop on GKE

Hands-on workshop curriculum for building, operating, and governing stateful AI agent fleets on [Agent Substrate](https://github.com/agent-substrate/substrate) (`v1.0.0`) on Google Kubernetes Engine (GKE).

This repository contains multi-track curriculum modules:

| Track | Title | Audience & Focus | Code & Guide |
|---|---|---|---|
| **101** | [Untrusted Code on Secure Runtimes: Agent Substrate on Kubernetes](101/README.md) | Platform engineers, Kubernetes operators, and AI infrastructure builders learning Substrate fundamentals. | [`101/`](101/) |
| **301** | [Agent Substrate 301: Enterprise AI Agents with Gemini Agent Platform on GKE](301/README.md) | Enterprise SRE, platform security, and AI agent developers building governed, stateful ADK agents across the Actor Boundary. | [`301/`](301/) |

---

## Curriculum Tracks

### [Track 101: Untrusted Code on Secure Runtimes](101/README.md)

Build and operate your own fleet of AI agents on Agent Substrate, starting from a bare GKE cluster. In ~85 minutes across 8 core modules:
- Install Agent Substrate and understand the roles of `ateapi`, `atelet`, `router`, and `egress`.
- Run sandboxed Go agents as Substrate **actors** inside `gVisor` sandboxes.
- Suspend and resume actors across worker nodes with millisecond warm restores from Cloud Storage memory snapshots.
- Connect agents to Gemini through an always-on gateway and enforce deny-by-default egress policies.
- Pack 20 concurrent agents onto 2 worker nodes using oversubscription, request parking, and idle auto-suspend.
- Monitor fleet metrics and latency with Prometheus, PromQL, and Cloud Monitoring dashboards.
- Dive deeper with 10 post-workshop optional labs in [`101/optional/`](101/optional/) covering `agent-substrate/env`, `google/ax`, custom idle triggers, snapshot branching, direct gRPC clients, HPA, agent swarms, Kata/Cloud-Hypervisor microVMs, and credential injection.

### [Track 301: Enterprise AI Agents with Gemini Agent Platform on GKE](301/README.md)

Build, upgrade, govern, trace, and evaluate a stateful **ADK Go v2** (`google.golang.org/adk/v2`) **Platform SRE Triage Agent** running inside an **Agent Substrate (`v1.0.0`)** gVisor Actor on GKE, integrated across the Actor Boundary with **Gemini Enterprise Agent Platform**. In ~90 minutes across 8 modules:
- Enforce the **Actor Boundary**: a 100% keyless, default-deny gVisor Incident Actor communicating strictly through an always-on **Governed Broker** (`sre-gateway`) serving a Google Cloud Console-styled **Incident Console**.
- Zero-wake state inspection with **Gemini Sessions** and cross-rollout / sibling-actor knowledge retention with **Gemini Memory Bank**.
- Dynamic, zero-rebuild runtime diagnostic tool delivery with SHA-256-verified **Skill Registry** procedural skill bundles (`v1` -> `v2`).
- A2A Agent Card publishing and MCP server discovery with **Agent Registry**, pausing high-blast-radius actions at an ADK **Remediation Gate** that suspends the Actor to Cloud Storage (`0 vCPU`) until operator approval.
- Defense-in-depth zero-trust governance: GKE `Gateway` + `GCPAgentGateway` + `GCPAuthzPolicy`, Pod **Agent Identity** SPIFFE credentials, **Auth Manager** 3-legged OAuth (`S256` PKCE), live **Model Armor** prompt-injection blocking (`INC-409`), and `0`-token snapshot hygiene audits.
- Export secret-scrubbed W3C **OpenTelemetry** GenAI spans to **Cloud Trace** and score live triage sessions with a pure-Go **Gemini on Agent Platform** autorater (`make eval`).

---

## Quick Start

Each track is self-contained with its own configuration, manifests, solutions, and tests.

```bash
# Clone the repository
git clone https://github.com/GoogleCloudPlatform/agent-substrate-workshop
cd agent-substrate-workshop

# Work on Track 101 (Fundamentals)
cd 101
cp workshop.env.example workshop.env
make preflight
make help

# Or work on Track 301 (Enterprise Platform)
cd ../301
cp workshop.env.example workshop.env
make preflight
make help
```

---

## Testing & Quality Assurance

Run offline test suites across both tracks from the root directory:

```bash
# Test both tracks
make test

# Test individual tracks
make test-101
make test-301
```

---

## Multi-Source Documentation Synchronization

Workshop documentation across both tracks is maintained and synchronized for three publishing formats:
1. **Modular Guide (`modules/`)**: Markdown sources of truth with step-by-step instructions, diagrams, and solution files.
2. **DevSite Codelabs (`codelabs/en.md`)**: Single-page `claat`-compatible codelabs with step timings and optional appendix labs.
3. **Google Skills / Qwiklabs (`qwiklabs/en.md`)**: Pre-warmed lab guides aligned with `CloudVLab/gcp-devrel-content` (`labs/gke005-agent-substrate-intro` for Track 101; `labs/gke006-agent-substrate-platform` for Track 301).

---

## License and Disclaimers

This project is licensed under the Apache License, Version 2.0 — see [LICENSE](LICENSE) for details and [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidelines.

This project is intended for demonstration purposes only. It is not intended for use in a production environment.

Use of Google Cloud services in this workshop is subject to the [Google Cloud Terms of Service](https://cloud.google.com/terms) and the [Google Cloud Service Specific Terms](https://cloud.google.com/terms/service-terms).

Eligibility for the [Google Open Source Software Vulnerability Rewards Program](https://bughunters.google.com/open-source-security) is determined by the [Google Open Source Software Vulnerability Reward Program Rules](https://bughunters.google.com/about/rules/open-source/google-open-source-software-vulnerability-reward-program-rules).
