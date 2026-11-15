# AGENTS.md

Context for AI coding agents working in `GoogleCloudPlatform/agent-substrate-workshop/301` (**Agent Substrate 301: Enterprise AI Agents with Gemini Agent Platform on GKE**).

## Project overview

Curriculum Track **301** of `GoogleCloudPlatform/agent-substrate-workshop`. Participants build and govern a stateful **ADK Go v2** (`google.golang.org/adk/v2`) **Platform SRE Triage Agent** running inside an **Agent Substrate (`v1.0.0`)** gVisor Actor on Google Kubernetes Engine (GKE), integrated across the Actor Boundary with **Gemini Enterprise Agent Platform** (Model Garden, Gemini Sessions, Gemini Memory Bank, Skill Registry, Agent Registry MCP & A2A, Agent Identity, Ingress `GCPAgentGateway` + governed broker with Model Armor & Auth Manager, and OpenTelemetry GenAI Traces & Online Evals).

## Branding & hygiene rules

- Always use **Gemini Enterprise Agent Platform** (or shorthand **Agent Platform** / **Gemini on Agent Platform**), never retired platform naming.
- Never hardcode personal GKE cluster, project, org, or node names in docs, Makefiles, manifests, or scripts; derive defaults from `workshop.env`, `kubectl config current-context`, or `gcloud config get-value project`.
- Never use internal-only compute terminology in public-facing prose, manifests, or comments (use "compute obtainability" where relevant).

## Multi-source documentation synchronization

Workshop content is maintained in **three synchronized places** (enforced automatically by `scripts/test/docs_sync_test.sh` during `make test`):

1. **Modular source of truth (`README.md`, `modules/00-setup/README.md`..`modules/07-wrap-up/README.md`, `modules/img/*.svg`)**:
   - Module titles use `# Title (NN min)` with subsections starting at `##` and diagram embeds at `../img/0N-*.svg`.
   - Modules `00` through `06` end with a `Next: [...]` relative link.
2. **DevSite Codelabs edition ([`codelabs/en.md`](codelabs/en.md) + `codelabs/img/*.svg`)**:
   - Starts with `claat` YAML frontmatter (`id: agent-substrate-301`, `categories: AgentPlatform, Kubernetes`, `language:GoLang`, `product:AgentPlatform`).
   - Contains a condensed `## Introduction` (`Duration: 02:00`), all 8 core modules (`## Setup` through `## Wrap-up and production patterns`, each immediately followed by `Duration: MM:SS` with subsections shifted down one level to `###`/`####`), and a trailing `## Appendix: Optional content` (`Duration: 10:00`) covering the GKE Ambient Networking + Secure Web Proxy (`AGENT_TO_ANYWHERE`) egress overlay and ending with `### When you're done`.
   - Rewrites diagram paths to `img/0N-*.svg`, omits `Next:` lines, links repository files via full GitHub URLs (`https://github.com/GoogleCloudPlatform/agent-substrate-workshop/blob/main/301/...`), and keeps zero `<...>` angle-bracket placeholders inside single-backtick inline code spans (`claat` HTML parser safety; enforce this across `modules/` and `qwiklabs/` as well).
3. **Google Skills / Qwiklabs edition ([`qwiklabs/en.md`](qwiklabs/en.md) + `qwiklabs/img/*.svg`)**:
   - Omits YAML frontmatter (starts directly at `# Agent Substrate 301...`), omits `Duration:` lines, and omits `## Appendix: Optional content` plus **all** references or links to optional overlays.
   - `## Setup` explains the pre-warmed Google Skills `GKE006` environment (Terraform pre-provisions `2× n1-standard-4` `Intel Skylake` on GKE `1.37`, regional Gateways, `ReasoningEngine`, `prod-sim`, and `.workshop/` state with `.prewarm/workshop-301-prewarm.tar.gz`) and omits `### Clean up`.
   - Keeps byte-for-byte identical copies of all Apache-2.0 header SVGs (`modules/img/`, `codelabs/img/`, `qwiklabs/img/`) with zero `@font-face` blocks or external URLs.

Whenever you edit instructions, commands, code snippets, or SVG diagrams in `modules/`, apply the corresponding update to `codelabs/` and `qwiklabs/`.

## Guided code edits & testing

- Starter `app/cmd/agent/main.go` carries `// WORKSHOP(module-1):` through `// WORKSHOP(module-4):`, and each `modules/01..04/solution/main.go` preserves all downstream `// WORKSHOP(...)` markers word-for-word (`scripts/test/solutions_test.sh`).
- Always run `make test` (`make test-go` + `make test-scripts`, including `lib_test.sh`, `preflight_test.sh`, `render_test.sh`, `solutions_test.sh`, `skill_publish_test.sh`, `governance_test.sh`, `observability_test.sh`, `hygiene_test.sh`, and `docs_sync_test.sh`) before committing.

## Agent skills

### Issue tracker

Issues and specs for this repo live in GitHub Issues (`gke-demos/substrate-agent-platform`) managed via the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Uses the five canonical triage labels (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context layout (`GLOSSARY.md` at repo root, UI design specification in `DESIGN.md`, and ADRs in `docs/adr/`). See `docs/agents/domain.md` and `DESIGN.md`.
