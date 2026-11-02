# AGENTS.md

This file provides guidance for AI coding agents (such as Antigravity) working in the Agent Substrate Workshop repository.

## Workshop Content & Multi-Source Synchronization

Workshop documentation is maintained in **three places** that must always be kept in sync whenever content changes:

1. **Source of truth (`modules/`, `optional/`, and `README.md`)**:
   - [`README.md`](README.md): Root workshop overview, agenda, prerequisites, and quick start.
   - `modules/00-setup/README.md` through `modules/07-wrap-up/README.md`: Individual READMEs for the 8 core workshop sections, alongside each module's `manifests/` and `solution/`.
   - `optional/A-env/README.md` through `optional/J-credential-injection/README.md`: Individual READMEs and supporting files for the 10 optional post-workshop labs.
2. **Codelabs edition ([`codelabs/en.md`](codelabs/en.md))**:
   - A single consolidated Markdown file formatted for Google Codelabs (`claat`), containing the introduction, all 8 core modules in order, and all 10 optional labs under a trailing `## Appendix: Optional content` section.
3. **Qwiklabs edition ([`qwiklabs/en.md`](qwiklabs/en.md))**:
   - A single consolidated Markdown file formatted identically to [`codelabs/en.md`](codelabs/en.md) for the introduction and all 8 core modules, **without** the trailing `## Appendix: Optional content` section or any references/links to optional labs.

### Rules when editing workshop content

Whenever you add, edit, or remove instructions, commands, explanations, or diagrams in the lab, **apply the change to the modular files (`modules/`, `optional/`, `README.md`), [`codelabs/en.md`](codelabs/en.md), and [`qwiklabs/en.md`](qwiklabs/en.md)** (for core module and introduction changes) while preserving the conventions below:

- **Section headers & `Duration` metadata**:
  - In `modules/NN-*/README.md`, the title is an H1 with timing in parentheses, e.g. `# Setup (10 min)`, and subsections start at H2 (`##`).
  - In [`codelabs/en.md`](codelabs/en.md) and [`qwiklabs/en.md`](qwiklabs/en.md), each core module is a top-level step using an H2 (`## Setup`) followed immediately by `Duration: MM:SS` (e.g. `Duration: 10:00`), and subsections shift down one level (`###`, `####`).
  - All optional labs in [`codelabs/en.md`](codelabs/en.md) live inside a single Codelab step (`## Appendix: Optional content`). Inside the appendix, each optional lab's title is an H3 (`### Optional lab A — ...`) and its subsections shift down two levels (`####`, `#####`) compared to `optional/*-*/README.md`. [`qwiklabs/en.md`](qwiklabs/en.md) omits this appendix altogether.
- **Image references & assets**:
  - Module READMEs reference diagrams as `![...](../img/NN-*.svg)` (stored in `modules/img/`).
  - [`codelabs/en.md`](codelabs/en.md) and [`qwiklabs/en.md`](qwiklabs/en.md) reference diagrams as `![...](img/NN-*.svg)` (stored in `codelabs/img/` and `qwiklabs/img/`).
  - If you add or modify an image in `modules/img/`, update the copies in `codelabs/img/` and `qwiklabs/img/` as well.
- **Links & navigation**:
  - Module READMEs end with a `Next: [...]` navigation line; **omit** these `Next:` lines in [`codelabs/en.md`](codelabs/en.md) and [`qwiklabs/en.md`](qwiklabs/en.md) for core modules (Codelabs and Qwiklabs provide their own step navigation).
  - Adjust relative paths for the file depth: links like `../../instructor/troubleshooting.md` in `modules/NN-*/README.md` become `instructor/troubleshooting.md` in [`codelabs/en.md`](codelabs/en.md) and [`qwiklabs/en.md`](qwiklabs/en.md).
  - Cross-references to optional labs inside [`codelabs/en.md`](codelabs/en.md) use the Codelab step anchor `(#9)` (the step index for `## Appendix: Optional content`) instead of relative links to `optional/*/README.md`.
  - In [`qwiklabs/en.md`](qwiklabs/en.md), **omit all references and links to optional labs** (including parenthetical callouts such as `(optional lab J)` or `(see optional lab G)`, the `### Keep going: optional labs` subsection in `Wrap-up`, and the `## Appendix: Optional content` step).
- **Intentional Codelabs/Qwiklabs-specific differences**:
  - `## Introduction` in [`codelabs/en.md`](codelabs/en.md) and [`qwiklabs/en.md`](qwiklabs/en.md) is a condensed version of [`README.md`](README.md) (it omits the agenda table, quick start, and teardown sections that duplicate step navigation).

---

## Repository Structure

| Path | Purpose |
|---|---|
| `modules/NN-*/` | Core workshop sections (`README.md`, `manifests/*.yaml.tmpl`, and `solution/main.go`). |
| `optional/*-*/` | Post-workshop optional labs (`README.md`, manifests, upstream patch scripts, and `solution/main.go`). |
| `codelabs/` | Consolidated Codelabs markdown (`en.md`, including optional labs appendix) and duplicated diagram assets (`img/`). |
| `qwiklabs/` | Consolidated Qwiklabs markdown (`en.md`, core modules only, no optional content appendix) and duplicated diagram assets (`img/`). |
| `app/` | Go module (`go.mod`) containing `cmd/agent` (edited by participants), `cmd/gateway`, `cmd/egressctl`, and shared packages in `internal/`. |
| `scripts/` | Bash & Python automation backing `Makefile` targets (`lib.sh`, `render.sh`, `catch-up.sh`, `verify/NN.sh`, `test/*_test.sh`). |
| `observability/` | Cloud Monitoring `dashboard.json`, `podmonitoring.yaml`, Prometheus UI deployment, and sample PromQL queries. |
| `instructor/` | Instructor `runbook.md` (event prep, timings, cut lines) and `troubleshooting.md`. |

---

## Code, Solutions, and Manifest Conventions

### Guided code edits (`WORKSHOP(...)` markers)
- Participants edit [`app/cmd/agent/main.go`](app/cmd/agent/main.go) during the workshop at locations marked with `// WORKSHOP(module-2):`, `// WORKSHOP(module-3):`, `// WORKSHOP(module-4):`, and `// WORKSHOP(lab-c):`.
- Completed agent files live in `modules/NN-*/solution/main.go` and `optional/*-*/solution/main.go`.
- **Marker preservation rule**: `make catch-up MODULE=<n>` copies a module's `solution/main.go` over `app/cmd/agent/main.go`. Therefore, every `modules/NN-*/solution/main.go` **must** preserve the exact `// WORKSHOP(...)` comment lines for all *subsequent* modules (`> NN`) and all optional labs word-for-word, and every `solution/main.go` must compile cleanly when copied to `app/cmd/agent/main.go`. This is enforced by [`scripts/test/solutions_test.sh`](scripts/test/solutions_test.sh).

### Manifest templates (`*.yaml.tmpl`)
- Kubernetes and Substrate manifest templates use `.yaml.tmpl` extensions and are rendered via [`scripts/render.sh`](scripts/render.sh), which substitutes environment variables (`PROJECT_ID`, `BUCKET_NAME`, `NAMESPACE`, `AGENT_IMAGE`, `WORKER_IMAGE`, etc.) from `workshop.env` and `.workshop/`.
- When changing template variables or manifest structure, check [`scripts/render.sh`](scripts/render.sh), [`scripts/catch-up.sh`](scripts/catch-up.sh), and [`scripts/test/render_test.sh`](scripts/test/render_test.sh).

### Catch-up and verification scripts
- Every core module `NN` has a verification script at `scripts/verify/NN.sh` (invoked by `make verify MODULE=N`) and catch-up logic in [`scripts/catch-up.sh`](scripts/catch-up.sh) (invoked by `make catch-up MODULE=N`).
- If you change resource names, template names (`agent-v1`, `agent-v2`, `agent-v3`), actor names (`hello`, `memo`, `buddy`, `swarm-01..20`), or ports in a module, update `scripts/catch-up.sh` and `scripts/verify/NN.sh` accordingly.

---

## Testing & Validation

Always run the offline test suite after changing Go code, solutions, manifests, dashboards, or shell scripts:

```bash
make test
```

This runs:
- `make test-go`: Runs `go vet ./...` and `go test ./...` inside `app/`.
- `make test-scripts`: Runs all offline shell and Python tests in `scripts/test/`:
  - [`scripts/test/lib_test.sh`](scripts/test/lib_test.sh) — unit tests for helper functions in `scripts/lib.sh`.
  - [`scripts/test/render_test.sh`](scripts/test/render_test.sh) — verifies that all `*.yaml.tmpl` manifests render cleanly without unresolved placeholders.
  - [`scripts/test/dashboards_test.sh`](scripts/test/dashboards_test.sh) — validates dashboard JSON files and PromQL cluster scoping.
  - [`scripts/test/solutions_test.sh`](scripts/test/solutions_test.sh) — verifies `WORKSHOP(...)` markers and compiles every `solution/main.go` against `app/`.
  - [`scripts/test/preflight_test.sh`](scripts/test/preflight_test.sh) — tests cluster preflight check logic.
