# AGENTS.md

Context for AI coding agents working in `GoogleCloudPlatform/agent-substrate-workshop`.

## Project Overview

Hands-on workshop repository for building, running, and governing stateful AI agent fleets on **Agent Substrate (`v1.0.0`)** on Google Kubernetes Engine (GKE).

The repository is structured into two self-contained workshops:
- **`intro/`**: *Untrusted Code on Secure Runtimes: Agent Substrate on Kubernetes* (Substrate fundamentals, Actor lifecycle, golden snapshots, suspend/resume, density/idle, and 10 advanced optional labs in `intro/optional/`).
- **`gemini-platform/`**: *Enterprise AI Agents with Agent Substrate and Gemini Agent Platform on GKE* (ADK Go v2 SRE Triage Agent, Actor Boundary, Gemini Sessions, Gemini Memory Bank, Skill Registry, Agent Registry MCP & A2A, Agent Identity, Ingress `GCPAgentGateway`, Model Armor & Auth Manager 3LO, and OpenTelemetry GenAI traces & online evals).

## Repository Layout

```
.
├── intro/                 # Intro workshop: Substrate Fundamentals
│   ├── app/               # Starter Go agent & gateway
│   ├── modules/           # 8 core modules (00-setup .. 07-wrap-up)
│   ├── optional/          # 10 post-workshop labs (A-env .. J-credential-injection)
│   ├── codelabs/          # Consolidated Codelabs edition (claat)
│   ├── qwiklabs/          # Google Skills / Qwiklabs edition (GKE004 & GKE005)
│   ├── observability/     # Dashboards and monitoring manifests
│   ├── instructor/        # Runbook and troubleshooting guide
│   ├── scripts/           # Makefile automation & verification tests
│   ├── Makefile           # Intro build and test orchestration
│   ├── AGENTS.md          # Intro agent guide and sync contract
│   └── README.md          # Intro curriculum landing page
├── gemini-platform/       # Gemini Platform workshop: Enterprise Agent Platform
│   ├── app/               # ADK Go v2 SRE triage agent, broker, & simulation binaries
│   ├── modules/           # 8 core modules (00-setup .. 07-wrap-up)
│   ├── codelabs/          # Consolidated Codelabs edition (claat)
│   ├── qwiklabs/          # Google Skills / Qwiklabs edition (GKE006)
│   ├── docs/              # ADRs (0001..0004) and architecture specifications
│   ├── instructor/        # Runbook and troubleshooting guide
│   ├── scripts/           # Makefile automation & verification tests
│   ├── Makefile           # Gemini Platform build and test orchestration
│   ├── AGENTS.md          # Gemini Platform agent guide and sync contract
│   └── README.md          # Gemini Platform curriculum landing page
├── Makefile               # Top-level delegation (make test -> intro + gemini-platform)
├── renovate.json          # Dependency tracking for intro/app/go.mod & gemini-platform/app/go.mod
├── CONTRIBUTING.md        # Open-source contribution guidelines
├── LICENSE                # Apache 2.0 license
├── README.md              # Multi-workshop landing page
└── AGENTS.md              # Top-level maintainer & AI agent instructions
```

## Branding & Hygiene Rules

- **Platform Branding**: Always use **Gemini Enterprise Agent Platform** (or shorthand **Agent Platform** / **Gemini on Agent Platform**), never retired platform naming.
- **Personal Identifiers**: Never hardcode personal GKE cluster, project, org, or node names in docs, Makefiles, manifests, or scripts; derive defaults from `workshop.env`, `kubectl config current-context`, or `gcloud config get-value project`.
- **Public Compute Terminology**: Never use internal compute terminology in public-facing prose, manifests, or comments (use "compute obtainability" where relevant).

## Multi-Source Documentation Synchronization

Workshop content across both tracks is maintained in three synchronized formats:

1. **Modular sources of truth (`NN-*/README.md`, `img/*.svg`)**:
   - Module titles use `# Title (NN min)` with subsections starting at `##` and diagram embeds at `../img/0N-*.svg`.
   - Core modules end with a `Next: [...]` relative link.
2. **DevSite Codelabs editions (`codelabs/en.md` + `codelabs/img/*.svg`)**:
   - Starts with `claat` YAML frontmatter (`language:GoLang`, `product:AgentPlatform`).
   - Contains a condensed `## Introduction`, core modules each followed by `Duration: MM:SS`, and an `## Appendix: Optional content` step ending with `### When you're done`.
   - Rewrites diagram paths to `img/0N-*.svg`, omits `Next:` lines, links repository files via full GitHub URLs (`https://github.com/GoogleCloudPlatform/agent-substrate-workshop/blob/main/<workshop>/...`), and keeps zero `<...>` angle-bracket placeholders inside single-backtick inline code spans (`claat` sets `shouldEsc = false` inside inline `<code>` spans and strips `<...>` as raw HTML tags).
   - Self-paced Setup (`modules/00-setup/README.md` & `codelabs/en.md`) must work in a fresh Google Cloud project and Cloud Shell without pre-warmed resources: persist `$(go env GOPATH)/bin` (after `go install github.com/google/ko@...`) and `$PWD/bin` in `~/.bashrc`; enable `container`, `monitoring`, `logging`, and `cloudtrace` APIs before `gcloud beta container clusters create`; pass `--disk-type pd-balanced --disk-size 100`; require uniform CPU platforms across nodes (warn against `e2-*` and document `n2-standard-4` / Skylake-pinned `n1-standard-4` fallbacks when `C3_CPUS` quota is unavailable); populate `workshop.env` via `sed -i` and run `gcloud container clusters get-credentials`; and keep `make install-status` in its own code block before any `kubectl ate` or `.workshop/` inspection commands.
3. **Google Skills / Qwiklabs editions (`qwiklabs/en.md` + `qwiklabs/img/*.svg`)**:
   - Omits YAML frontmatter (starts directly with `# Title`), omits `Duration:` lines, and omits `## Appendix: Optional content` plus all references/links to optional overlays.
   - Synchronized with `CloudVLab/gcp-devrel-content`:
     - **Intro Workshop**: `labs/gke005-agent-substrate-intro` (and `labs/gke004-kubecon-2026`). Enforced by `intro/scripts/test/qwiklabs_test.sh`.
     - **Gemini Platform Workshop**: `labs/gke006-agent-substrate-platform`. Pre-provisions non-Substrate infrastructure in Terraform and `substrate-prewarm` Job, unpacks `.prewarm/gemini-platform-prewarm.tar.gz`, and omits `### Clean up`. Enforced by `gemini-platform/scripts/test/docs_sync_test.sh`.

Whenever editing instructions, code blocks, or SVG diagrams in `modules/`, apply matching updates to `codelabs/` and `qwiklabs/`.

## Testing & Quality Assurance

Test suites are fully decoupled and can be run individually or together:

```bash
# Run tests across all workshops
make test

# Run Intro workshop test suite
make -C intro test

# Run Gemini Platform workshop test suite
make -C gemini-platform test

# Run Go race detector on workshop applications
(cd intro/app && go test -race -count=1 ./...)
(cd gemini-platform/app && go test -race -count=1 ./...)
```
