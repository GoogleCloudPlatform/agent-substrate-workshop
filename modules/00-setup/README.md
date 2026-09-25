# Module 0 — Setup (10 min)

**Goal:** point the workshop at your GCP project, create the cloud resources Substrate needs,
and start the Substrate install in the background. It runs while you take the architecture tour in Module 1.

## What you need
- A GCP project where you are Owner, and a GKE **1.37+** cluster in it. The cluster needs Workload Identity,
  managed OpenTelemetry, no node auto-upgrade, and no spot nodes. Your instructor created it, or you can follow
  [the substrate GCP guide](https://github.com/agent-substrate/substrate/tree/main/tools/setup-gcp).
- `kubectl` pointed at that cluster: `kubectl config current-context`.
- Locally installed: `gcloud`, `kubectl`, `go` 1.27+, `ko`, `jq`, `envsubst` (gettext), `nc`, `curl`, `git`, `python3`.

## Steps

1. **Configure.** Copy the env file and set `PROJECT_ID`. Every other value has a working default.
   ```bash
   cp workshop.env.example workshop.env
   $EDITOR workshop.env
   ```
2. **Preflight.** This checks your tools and the cluster's version, Workload Identity, managed OTel,
   and node pool settings. Fix anything it flags before you continue.
   ```bash
   make preflight
   ```
3. **Cloud resources.** This creates a GCS bucket (snapshots live here), an Artifact Registry repo (your images live here),
   and IAM bindings for Substrate's components, your gateway, and the Prometheus UI.
   ```bash
   make gcp
   ```
4. **Start the install in the background.** It clones substrate at `SUBSTRATE_REF`, builds `kubectl ate`,
   deploys `ate-system`, pins a worker image that matches the control plane, labels your nodes, and
   raises the router's parked-request budget.
   ```bash
   make install-bg
   make install-status     # run any time; the install takes about 5–10 minutes
   ```
5. **Dashboards.** This creates the workshop dashboard and substrate's three dashboards in Cloud Monitoring.
   They stay empty until Module 6. Setting them up now means data is already flowing by then.
   ```bash
   make dashboards
   ```
6. **Local tools.** This builds `bin/egressctl`, which you'll use in Module 4.
   ```bash
   make tools
   ```

## What just happened
- `workshop.env` is the only file you edit for configuration. Scripts write what they learn (image
  digests, the substrate version, tokens) into `.workshop/`, which is gitignored.
- Substrate needs three things from GCP: a **bucket** for snapshots, **Workload Identity** so its
  components can write to that bucket, and a **cluster new enough** for Pod certificates and ClusterTrustBundles,
  which Substrate uses to give every actor an identity.

## Checkpoint
```bash
make verify MODULE=0
```

**Stuck?** Run `make catch-up MODULE=1`, which does all of this in the foreground, and see
[troubleshooting](../../instructor/troubleshooting.md).

Next: [Module 1 — Architecture tour](../01-architecture/README.md)
