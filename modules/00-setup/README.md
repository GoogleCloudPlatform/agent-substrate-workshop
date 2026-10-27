# Setup (10 min)

The goal of this section is to point the workshop at your Google Cloud project, create the cloud resources that Substrate needs,
and start the Substrate installation in the background. The installation runs while you take the architecture tour.

## What you need
- A Google Cloud project where you have the **Owner** role, and a GKE **1.37+** cluster in that project. The cluster requires Workload Identity,
  managed OpenTelemetry, Managed Service for Prometheus, no node auto-upgrade (or an active maintenance exclusion), and no spot nodes. Use the cluster that your instructor created, or create one using the commands in the [instructor runbook](../../instructor/runbook.md#one-week-before) (see also the [Substrate Google Cloud guide](https://github.com/agent-substrate/substrate/tree/main/tools/setup-gcp)).
- `kubectl` configured to point at that cluster (`kubectl config current-context`).
- Locally installed tools: `gcloud`, `kubectl`, `go` 1.27+, `ko`, `jq`, `envsubst` (gettext), `nc`, `curl`, `git`, `python3`,
  and a running **Docker** daemon with `buildx` (the installation builds one Substrate image from a Dockerfile). Cloud Shell includes all of these tools.

## Steps

1. **Configure the environment.** Copy the example environment file and set `PROJECT_ID` and `CLUSTER_LOCATION` to your cluster's exact zone or
   region from `gcloud container clusters list` (a zonal cluster such as `us-central1-a` won't match
   `us-central1`). If your cluster isn't named `ate-cluster`, set `CLUSTER_NAME` as well. Every other
   variable has a working default.
   ```bash
   gcloud container clusters list --project YOUR_PROJECT_ID    # NAME and LOCATION
   cp workshop.env.example workshop.env
   $EDITOR workshop.env
   ```
2. **Run preflight checks.** Verify your local tools and the cluster's version, Workload Identity, managed OpenTelemetry,
   and node pool settings. Fix any issues that the script flags before you continue.
   ```bash
   make preflight
   ```
3. **Create Google Cloud resources.** Create a Cloud Storage bucket for snapshots, an Artifact Registry repository for your container images,
   and IAM bindings for Substrate's components, your gateway, and the Prometheus UI. The script grants image pull access
   (`roles/artifactregistry.reader`) on that single repository, not the entire project.
   ```bash
   make gcp
   ```
4. **Add `bin/` to your `PATH`.** The installer builds the `kubectl ate` plugin into `bin/kubectl-ate`. The
   `make` targets find it automatically, but `make install-status` and the "Hello actor" section onwards expect `kubectl ate`
   on your shell's `PATH`. Run the following command from the repository root:
   ```bash
   export PATH="$PWD/bin:$PATH"
   ```
   This setting applies only to your current terminal session. To persist it, add the same line with the absolute repository path instead of `$PWD` to
   `~/.zshrc` or `~/.bashrc`. If you use direnv, add `PATH_add bin` to an `.envrc` file at the repository root.
5. **Start the installation in the background.** This target clones Substrate at `SUBSTRATE_REF`, builds `kubectl ate`,
   deploys `ate-system`, pins a worker image that matches the control plane, labels your nodes, and
   increases the router's parked-request budget and shutdown grace period.
   ```bash
   make install-bg
   make install-status     # check progress at any time; the install takes about 5–10 minutes
   ```
   After the installation finishes, `kubectl ate --help` works. Running `make verify MODULE=0` verifies that `bin/` is on your `PATH`.
6. **Create the dashboards.** Create the workshop dashboard and Substrate's three dashboards in Cloud Monitoring.
   They remain empty until the "Observe the fleet" section, but creating them now ensures that data is already flowing when you get there.
   ```bash
   make dashboards
   ```
7. **Build local tools.** Build `bin/egressctl`, which you use in the "Make it an agent" section.
   ```bash
   make tools
   ```

## What just happened
- `workshop.env` is the only file you edit for configuration. Scripts write generated state (image
  digests, the Substrate version, and tokens) into `.workshop/`, which Git ignores.
- Substrate requires three things from Google Cloud: a **Cloud Storage bucket** for snapshots, **Workload Identity** so its
  components can write to that bucket, and a **GKE cluster version** that supports Pod certificates and ClusterTrustBundles,
  which Substrate uses to give every actor an identity.

## Checkpoint
```bash
make verify MODULE=0
```

**Stuck?** Run `make catch-up MODULE=1` to run all of these steps in the foreground, and see
[troubleshooting](../../instructor/troubleshooting.md).

Next: [Architecture tour](../01-architecture/README.md)
