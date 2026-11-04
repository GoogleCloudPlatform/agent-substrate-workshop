# Agent Substrate Workshop

Build and operate your own fleet of AI agents on [Agent Substrate](https://github.com/agent-substrate/substrate),
starting from a bare GKE cluster. In about 85 minutes you will:

- install Substrate, and learn what each of its components does;
- build a Go agent, run it as a Substrate **actor**, and suspend and resume it across workers;
- give it a model (Gemini on the Gemini Enterprise Agent Platform) through an always-on gateway, and lock down its egress;
- run 20 agents on 2 workers with request parking and idle suspend;
- watch the whole fleet in Prometheus and Cloud Console dashboards.

Everything you deploy, you build: the agent, the gateway, and (in source mode) Substrate itself go into your own Artifact Registry.

## Agenda

| # | Module | Min | You'll learn |
|---|---|---|---|
| 0 | [Setup](modules/00-setup/README.md) | 10 | GCP prerequisites; the install starts in the background |
| 1 | [Architecture tour](modules/01-architecture/README.md) | 10 | Actors vs Pods; ateapi, atelet, router, egress; CRDs vs records |
| 2 | [Hello actor](modules/02-hello-actor/README.md) | 15 | WorkerPool, ActorTemplate + golden snapshot, Actor, routing header |
| 3 | [Suspend and resume](modules/03-suspend-resume/README.md) | 12 | MEMORY vs VOLUMES snapshots, durable dirs, template immutability |
| 4 | [Make it an agent](modules/04-make-it-an-agent/README.md) | 13 | Gateway pattern, Workload Identity, deny-by-default egress |
| 5 | [Density and idle](modules/05-density-idle/README.md) | 10 | Oversubscription, parking, building an idle-suspender |
| 6 | [Observe the fleet](modules/06-observe/README.md) | 12 | Substrate OTel + app metrics, PromQL, dashboards, per-actor logs |
| 7 | [Wrap-up](modules/07-wrap-up/README.md) | 3 | Real-world patterns, limits, optional labs |

Optional labs afterwards, in order: each one changes the cluster more than the one before, so a later lab
can't break an earlier one (see [Module 7](modules/07-wrap-up/README.md#keep-going-optional-labs)).
[A — agent-substrate/env](optional/A-env/README.md) (start here), [B — google/ax](optional/B-ax/README.md),
[C](optional/C-what-counts-as-idle/README.md), [D](optional/D-tags-clone-revert/README.md), [E](optional/E-grpc-client/README.md) stay in their own namespace;
[F](optional/F-observability-deep-dive/README.md), [G](optional/G-hpa-workerpool/README.md),
[H](optional/H-agent-swarm/README.md), [J](optional/J-credential-injection/README.md) touch shared cluster pieces and have a clean-up;
[I](optional/I-microvm/README.md) adds a nested-virtualization node pool and a cluster-wide `SandboxConfig`.

## Prerequisites
- A GCP project where you are **Owner**, with a GKE **1.37+** cluster that has Workload Identity, managed
  OpenTelemetry, Managed Prometheus, no node auto-upgrade, no spot nodes, 2× `c3-standard-4`. `make preflight`
  checks all of it.
- Local tools: `gcloud`, `kubectl`, `go` 1.27+, `ko`, `jq`, `envsubst`, `curl`, `git`, `python3`, and, only to
  build substrate from source, a running Docker with `buildx`. Cloud Shell has all of them except `ko`
  (`go install github.com/google/ko@latest`).
- Comfort with Kubernetes. No prior Substrate knowledge needed.

For comparison, [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) solves a
similar problem with a Pod per agent session and no Substrate integration — [Module 1](modules/01-architecture/README.md)
has the full comparison.

## Two ways to take it
- **Instructor-led:** follow the modules in order. If you fall behind, `make catch-up MODULE=<n>` brings your
  environment to the start of module `n`, and you keep going.
- **Self-paced:** same path. After each module, `make verify MODULE=<n>` checks that you got there.
  `instructor/troubleshooting.md` covers the common failures. Put `bin/` on your PATH
  (`export PATH="$PWD/bin:$PATH"` from the repo root, Module 0 step 5) before you type any `kubectl ate …`
  yourself. `make catch-up` doesn't do that for your shell.

## Quick start
```bash
cp workshop.env.example workshop.env     # set PROJECT_ID and CLUSTER_LOCATION
make preflight
export PATH="$PWD/bin:$PATH"             # `kubectl ate` lands in bin/ during the install (Module 0 step 5)
make help                                # every target, one line each
```

## Repo layout

| Path | What |
|---|---|
| `modules/NN-*/` | The guide: one README per module, the manifests it uses, and the finished `main.go` |
| `app/` | Go code: `cmd/agent` (you edit this), `cmd/gateway`, `cmd/egressctl` |
| `scripts/` | Everything `make` runs; `verify/` checks each module's end state |
| `observability/` | Dashboard JSON, PodMonitoring, Prometheus UI, canned queries |
| `optional/` | Post-workshop labs |
| `instructor/` | Runbook (timings, cut lines, pre-event checks) and troubleshooting |

## Versions
Substrate moves fast. The workshop pins it with `SUBSTRATE_REF` in `workshop.env`. It uses published images when
`SUBSTRATE_IMAGE_REPO`/`SUBSTRATE_IMAGE_TAG` are set, and builds from source otherwise. The runbook says which ref
was validated for the next event. The workshop is validated on GKE 1.37, and `make preflight` rejects anything older.

## Clean up
`make teardown` deletes the workshop's atespace, namespace, dashboards (the workshop's and substrate's three), and snapshots, and keeps your cluster,
bucket, and Artifact Registry — it asks you to confirm unless you set `YES=1`. `YES=1 scripts/teardown.sh --all`
also removes the bucket and the Artifact Registry repo (with the grants `make gcp` put on them), the
project IAM bindings `make gcp` created for the workshop namespace, and the snapshot transfer service account, but never
the GKE cluster; see [Module 7](modules/07-wrap-up/README.md#clean-up).

If the project exists only for this workshop, deleting the project is the simplest complete clean up. Otherwise, run
`make teardown ALL=1`, then `make delete-cluster`, which deletes the cluster's PersistentVolumeClaims first so their
disks (Substrate's Postgres volume among them) don't outlive the cluster. At an instructor-led event, check with your
instructor before you delete a cluster they provisioned.

## License and Disclaimers

This project is licensed under the Apache License, Version 2.0 — see [LICENSE](LICENSE) for details and [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidelines.

This project is intended for demonstration purposes only. It is not intended for use in a production environment.

Use of Google Cloud services in this workshop is subject to the [Google Cloud Terms of Service](https://cloud.google.com/terms) and the [Google Cloud Service Specific Terms](https://cloud.google.com/terms/service-terms).

Eligibility for the [Google Open Source Software Vulnerability Rewards Program](https://bughunters.google.com/open-source-security) is determined by the [Google Open Source Software Vulnerability Reward Program Rules](https://bughunters.google.com/about/rules/open-source/google-open-source-software-vulnerability-reward-program-rules).
