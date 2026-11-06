# Agent Substrate Workshop

Build and operate your own fleet of AI agents on [Agent Substrate](https://github.com/agent-substrate/substrate),
starting from a bare GKE cluster. In about 85 minutes, you:

- Install Substrate and learn what each of its components does.
- Build a Go agent, run it as a Substrate **actor**, and suspend and resume it across workers.
- Connect the agent to a model (Gemini on the Gemini platform) through an always-on gateway, and lock down its egress.
- Run 20 agents on 2 workers with request parking and idle suspend.
- Monitor the fleet in Prometheus and Google Cloud console dashboards.

You build everything you deploy: the agent, the gateway, and (in source mode) Substrate itself go into your own Artifact Registry repository.

This workshop uses billable resources: a GKE cluster with two `c3-standard-4` nodes and a 500 GiB persistent disk for
Substrate's bundled Postgres, plus Cloud Monitoring, Cloud Storage, and Artifact Registry. Use a dedicated project that you
can delete when you finish; the "Clean up" section below shows how.

## Agenda

| # | Section | Min | What you learn |
|---|---|---|---|
| 0 | [Setup](modules/00-setup/README.md) | 10 | Google Cloud prerequisites; starting the background install |
| 1 | [Architecture tour](modules/01-architecture/README.md) | 10 | Actors vs. Pods; ateapi, atelet, router, egress; CRDs vs. records |
| 2 | [Hello actor](modules/02-hello-actor/README.md) | 15 | WorkerPool, ActorTemplate and golden snapshot, Actor, routing header |
| 3 | [Suspend and resume](modules/03-suspend-resume/README.md) | 12 | MEMORY vs. VOLUMES snapshots, durable directories, template immutability |
| 4 | [Make it an agent](modules/04-make-it-an-agent/README.md) | 13 | Gateway pattern, Workload Identity, deny-by-default egress |
| 5 | [Density and idle](modules/05-density-idle/README.md) | 10 | Oversubscription, request parking, building an idle-suspender |
| 6 | [Observe the fleet](modules/06-observe/README.md) | 12 | Substrate OpenTelemetry and app metrics, PromQL, dashboards, per-actor logs |
| 7 | [Wrap-up](modules/07-wrap-up/README.md) | 3 | Real-world patterns, limits, optional labs |

The optional labs follow in order of how much they change the cluster, so a later lab
doesn't break an earlier one (see [Wrap-up](modules/07-wrap-up/README.md#keep-going-optional-labs)):
- [A — agent-substrate/env](optional/A-env/README.md) (start here), [B — google/ax](optional/B-ax/README.md),
  [C](optional/C-what-counts-as-idle/README.md), [D](optional/D-tags-clone-revert/README.md), and [E](optional/E-grpc-client/README.md) stay in their own namespace.
- [F](optional/F-observability-deep-dive/README.md), [G](optional/G-hpa-workerpool/README.md),
  [H](optional/H-agent-swarm/README.md), and [J](optional/J-credential-injection/README.md) modify shared cluster resources and include cleanup steps.
- [I](optional/I-microvm/README.md) adds a nested-virtualization node pool and a cluster-wide `SandboxConfig`.

## Prerequisites
- A Google Cloud project where you have the **Owner** role, with a GKE **1.37+** cluster that has Workload Identity, managed
  OpenTelemetry, Managed Service for Prometheus, no node auto-upgrade, no spot nodes, and 2× `c3-standard-4` nodes. `make preflight`
  verifies all of these settings. If you don't have a cluster yet, the [Setup](modules/00-setup/README.md) section creates one
  with these settings.
- Local tools: `gcloud`, `kubectl`, `go` 1.27+, `ko`, `jq`, `envsubst`, `curl`, `git`, `python3`, and, only to
  build Substrate from source, a running Docker daemon with `buildx`. Cloud Shell includes all of these tools except `ko`
  (`go install github.com/google/ko@v0.19.1`).
- Familiarity with Kubernetes. You don't need prior Substrate knowledge.

For comparison, [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) solves a
similar problem with a Pod per agent session and no Substrate integration. See the [Architecture tour](modules/01-architecture/README.md)
for the full comparison.

## Two ways to take this workshop
- **Instructor-led:** Follow the sections in order. If you fall behind, run `make catch-up MODULE=<n>` to bring your
  environment to the start of section `n`, and continue from there.
- **Self-paced:** Follow the same path. After each section, run `make verify MODULE=<n>` to verify your state.
  See `instructor/troubleshooting.md` for common failures. Add `bin/` to your `PATH`
  (`export PATH="$PWD/bin:$PATH"` from the repository root, as shown in the [Setup](modules/00-setup/README.md) section) before you run any `kubectl ate …`
  commands yourself; `make catch-up` doesn't modify your shell environment.

## Quick start
```bash
cp workshop.env.example workshop.env     # set PROJECT_ID and CLUSTER_LOCATION
make preflight
export PATH="$PWD/bin:$PATH"             # `kubectl ate` lands in bin/ during the install
make help                                # list every target
```

## Repository layout

| Path | Description |
|---|---|
| `modules/NN-*/` | The guide: one README per section, the manifests it uses, and the finished `main.go` |
| `app/` | Go code: `cmd/agent` (which you edit), `cmd/gateway`, `cmd/egressctl` |
| `scripts/` | Scripts that `make` runs; `verify/` checks each section's end state |
| `observability/` | Dashboard JSON, PodMonitoring, Prometheus UI, canned queries |
| `optional/` | Post-workshop labs |
| `instructor/` | Runbook (timings, cut lines, pre-event checks) and troubleshooting |

## Versions
Substrate evolves quickly. The workshop pins a specific commit with `SUBSTRATE_REF` in `workshop.env`. It uses published images when
you set `SUBSTRATE_IMAGE_REPO` and `SUBSTRATE_IMAGE_TAG`, and builds from source otherwise. The runbook lists the ref
validated for the next event. This workshop requires GKE 1.37 or later, and `make preflight` rejects older versions.

## Clean up
`make teardown` deletes the workshop's atespace, namespace, dashboards (the workshop dashboard and Substrate's three dashboards), and snapshots, while keeping your cluster,
bucket, and Artifact Registry repository. It prompts for confirmation unless you set `YES=1`. Running `YES=1 scripts/teardown.sh --all`
also deletes the bucket, the Artifact Registry repository, the
project IAM bindings that `make gcp` created for the workshop namespace, and the snapshot transfer service account, but never deletes
the GKE cluster. See [Wrap-up](modules/07-wrap-up/README.md#clean-up) for details.

If the project exists only for this workshop, deleting the project is the simplest complete cleanup. Otherwise, run
`make teardown ALL=1`, and then run `make delete-cluster`, which deletes the cluster's PersistentVolumeClaims first so their
disks (including Substrate's Postgres volume) don't outlive the cluster. At an instructor-led event, check with your
instructor before you delete a cluster that they provisioned.

## License and Disclaimers

This project is licensed under the Apache License, Version 2.0 — see [LICENSE](LICENSE) for details and [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidelines.

This project is intended for demonstration purposes only. It is not intended for use in a production environment.

Use of Google Cloud services in this workshop is subject to the [Google Cloud Terms of Service](https://cloud.google.com/terms) and the [Google Cloud Service Specific Terms](https://cloud.google.com/terms/service-terms).

Eligibility for the [Google Open Source Software Vulnerability Rewards Program](https://bughunters.google.com/open-source-security) is determined by the [Google Open Source Software Vulnerability Reward Program Rules](https://bughunters.google.com/about/rules/open-source/google-open-source-software-vulnerability-reward-program-rules).
