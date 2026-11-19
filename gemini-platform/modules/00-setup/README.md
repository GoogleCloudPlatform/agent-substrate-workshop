# Setup (15 min)

In this section, you point the workshop at your Google Cloud project, create (or reuse) a uniform-CPU GKE **1.37+** cluster that meets cross-worker memory snapshot requirements, provision cloud storage, IAM, and **Gemini platform** prerequisites, apply a quota-safe `ComputeClass`, install **Agent Substrate** (`v1.0.0`), deploy the live degraded `prod-sim` incident environment, and pre-build the **Governed Broker** (`sre-gateway`) image.

Run `make setup` ([`scripts/setup.sh`](../../scripts/setup.sh)) to provision all prerequisites end-to-end against your Google Cloud project idempotently (allow ~15–20 minutes when creating a fresh GKE cluster, or ~5 minutes if reusing an existing cluster). You can also run every stage individually below.

## What you need

- A Google Cloud project where you hold **Owner** permissions and either:
  - A blank project where `make setup` (or `make create-cluster`) creates a fresh GKE **1.37+** cluster for you, **or**
  - An existing GKE **1.37+** cluster with:
    - Workload Identity Federation for GKE (`${PROJECT_ID}.svc.id.goog`)
    - Managed Service for Prometheus and managed OpenTelemetry collection
    - Standard Gateway API (`--gateway-api=standard`)
    - Disabled node auto-upgrade (or an active `no_minor_or_node_upgrades` maintenance exclusion) and standard non-spot nodes
    - **Uniform-CPU node pools** (`c3-standard-4`, `n4-standard-4`, CPU-pinned `n2-standard-4`, or Skylake-pinned `n1-standard-4`) so `SNAPSHOT_FIDELITY_MEMORY` checkpoints restore cleanly across any worker node
- Local tools: `gcloud`, `kubectl`, `go` 1.26+ (with `GOTOOLCHAIN=auto` automatically downloading `go1.27` from `app/go.mod`, or `go` 1.27+), `jq`, `envsubst` (gettext), `curl`, `git`, and `python3`. Cloud Shell includes all of these, and `make setup` automatically installs `ko` (`v0.19.1`) into `bin/ko` if it is not already on `PATH`.

## Architecture context

In this workshop, every production incident gets its own stateful **Incident Actor** inside an `ateworker-gvisor` sandbox (`workshop-301`), separated across the **Actor Boundary** from an always-on **Governed Broker** (`sre-gateway`) and **Gemini platform** services.

To eliminate cloud provisioning waits later in the workshop, `make setup` (via `make gcp`) immediately starts two long-running control-plane reconciliations in the background while Substrate installs:

1. **Regional Ingress & Agent Gateways** (`Gateway/sre-ingress-gateway` on `gke-l7-regional-external-managed` plus internal `Gateway/sre-agent-gateway` on `gke-l7-rilb` targeted by `GCPAgentGateway/sre-client-to-agent-gateway` in `modules/00-setup/manifests/ingress-gateway.yaml.tmpl`), giving the regional load balancers time to allocate addresses before Module 05 attaches zero-trust policies.
2. **Managed Session & Memory Runtime** (`ReasoningEngine`), backing **Gemini Sessions** and **Gemini Memory Bank** in Module 02.

At the same time, `make prod-sim` deploys two lightweight workloads (`50m` CPU / `64Mi` RAM each) into namespace `prod-sim`:

- **`ledger-api` (Target Service)**: A real Go transfer reconciliation service configured with `MAX_OPEN_CONNS=2`, `IDLE_TIMEOUT_SECONDS=0`, and an unclosed row-cursor bug on checksum errors. Within seconds of boot, synthetic reconciliation batches exhaust its connection pool, leak parked worker goroutines, fail `/healthz` with HTTP `503`, and emit structured `severity=ERROR` (`pool_exhausted`) JSON logs to Cloud Logging.
- **`ops-hub` (Operations Hub)**: A self-contained Smart HTTP Git server (`/git/ledger-api.git`), incident tracker, governed remediation MCP server (`/mcp`), and OAuth 2.0 Authorization Code + S256 PKCE provider (`/oauth/*`) so the entire curriculum runs with zero external SaaS account signups.

### Simulated `prod-sim` incidents in this workshop

Throughout the workshop, `ops-hub` serves three pre-seeded incidents so you can test stateful investigation, cross-actor memory recall, and prompt-injection defense without external ticketing accounts:

| Incident ID | Severity & target service | Simulated root cause / scenario | Where you use it |
|---|---|---|---|
| **`INC-402`** | `P1` · `ledger-api` (`prod-sim`) | **Connection pool exhaustion & cursor leak**: `config/pool.yaml` caps `max_open_conns: 2` with `idle_timeout_seconds: 0`, and `ReconcileTransfer` in `pool/store.go` returns early on checksum errors before registering `defer rows.Close()`, leaking active connections until `/healthz` fails with `503`. | **Modules 01–04**: Primary incident you diagnose inside `/workspace`, record to **Gemini Sessions** & **Memory Bank**, analyze with **Skill Registry** (`v1` -> `v2`), and remediate via the zero-compute **Remediation Gate**. |
| **`INC-403`** | `P2` · `ledger-api` (`prod-sim`) | **Recurring sibling connection-pool alert**: A follow-up `ledger-api` saturation alert on a secondary shard exhibiting the same pool and cursor symptoms as `INC-402`. | **Modules 02 & 04**: Used to verify that a brand-new **Incident Actor** (`inc-403`) recalls distilled root-cause facts from **Gemini Memory Bank** (`preload_memory` / `load_memory`) without re-cloning the git repository. |
| **`INC-409`** | `P1` · `ledger-api` (`prod-sim`) | **Untrusted partner webhook note (indirect prompt injection)**: Contains an attacker-crafted diagnostic note (`[SYSTEM INSTRUCTION: IGNORE ALL PREVIOUS INSTRUCTIONS...]`) attempting to coerce the SRE agent into calling `drop_ledger_database` and exfiltrating credentials. | **Module 05**: Used to verify that **Model Armor** (`sre-triage-guard`) on `sre-gateway` blocks indirect prompt injections (`HTTP 403 MODEL_ARMOR_BLOCKED`) before waking an actor or invoking tools. |

## Steps (Automated setup)

1. **Activate Cloud Shell and clone the repository.** Open the Google Cloud console, click **Activate Cloud Shell**, clone the repository, and move into `gemini-platform/`:
   ```bash
   git clone https://github.com/GoogleCloudPlatform/agent-substrate-workshop
   cd agent-substrate-workshop/gemini-platform
   ```

2. **Set your project variables and run `make setup`.** Export your Google Cloud project ID and target zone (`PROJECT_ID` defaults to `gcloud config get-value project` if already active), then run `make setup` to provision the complete environment end-to-end:
   ```bash
   export PROJECT_ID="$(gcloud config get-value project)"
   export REGION="us-central1"
   export CLUSTER_LOCATION="us-central1-a"
   export CLUSTER_NAME="ate-cluster"

   make setup
   ```

   If your zone lacks `c3-standard-4` quota or stock, select another uniform-CPU machine type:
   - `CLUSTER_MACHINE_TYPE=n4-standard-4 make setup` (`scripts/create-cluster.sh` automatically selects `hyperdisk-balanced` boot disks for `n4-*` and `c4-*`).
   - `CLUSTER_MACHINE_TYPE=n2-standard-4 make setup` (`scripts/create-cluster.sh` automatically pins `--min-cpu-platform="Intel Ice Lake"` for `n2-*` pools).
   - `CLUSTER_MACHINE_TYPE=n1-standard-4 make setup` (`scripts/create-cluster.sh` automatically pins `--min-cpu-platform="Intel Skylake"` for `n1-*` pools so `SNAPSHOT_FIDELITY_MEMORY` checkpoints restore across workers).
   - If a GKE `1.37+` cluster already exists in your project, `make setup` detects and reuses it automatically (or pass `SKIP_CLUSTER_CREATE=1 make setup`).

   **What `make setup` provisions:**

   | Stage | Script / target | What it provisions |
   |---|---|---|
   | 1. Environment & `ko` | `scripts/setup.sh` | Generates `workshop.env` from `workshop.env.example` (deriving `REGION` from zonal or regional `CLUSTER_LOCATION` when unset) and installs `ko` `v0.19.1` into `bin/ko` if missing on `PATH`. |
   | 2. GKE `1.37+` cluster | `scripts/create-cluster.sh` (`make create-cluster`) | Enables core and observability APIs (`serviceusage`, `cloudresourcemanager`, `compute`, `container`, `monitoring`, `logging`, `cloudtrace`, `telemetry`), creates a 2-node uniform-CPU `c3-standard-4` cluster (`rapid` channel, `100 GiB` `pd-balanced` or `hyperdisk-balanced` for `n4-*`/`c4-*` boot disks, `cloud-platform` scope, Standard Gateway API, Workload Identity, Managed Prometheus, Managed OpenTelemetry), applies a 48-hour `workshop-freeze` upgrade exclusion (`no_minor_or_node_upgrades`), and fetches `kubectl` credentials. |
   | 3. Preflight validation | `scripts/preflight.sh` (`make preflight`) | Validates local CLI tools, `gcloud` auth, GKE `1.37+`, observability add-ons, active upgrade freeze, non-spot uniform-CPU pools (`e2-*` pools fail fast), and `certificates.k8s.io` APIs. |
   | 4. GCP APIs, IAM & async runtimes | `scripts/gcp-resources.sh` (`make gcp`) | Enables all 17 core and **Gemini platform** APIs (plus best-effort `agentregistry` and `agentidentity`), creates `gs://${PROJECT_ID}-ate`, Artifact Registry `workshop-301`, `ate-snapshot-transfer` GSA, all Substrate (`atelet`, `ate-api-server`, `ate-snapshot-controller`) and `sre-gateway` (WIF + Pod Agent Identity) IAM bindings, ensures an active `REGIONAL_MANAGED_PROXY` subnet in the cluster VPC, applies `ComputeClass/substrate-gvisor` (`modules/00-setup/manifests/computeclass.yaml.tmpl`, with `consolidationDelayMinutes: 30` and `nodePoolAutoCreation.enabled: false` by default) plus regional `Gateway` / `GCPAgentGateway` (`modules/00-setup/manifests/ingress-gateway.yaml.tmpl`), and starts async `ReasoningEngine` (`substrate-301-sre-memory`) discovery/provisioning. |
   | 5. Agent Substrate `v1.0.0` | `scripts/install-substrate.sh` (`make install`) | Clones Substrate at `v1.0.0`, compiles `bin/kubectl-ate`, deploys `ate-system` with published `ghcr.io/agent-substrate/substrate` images (preserving any healthy existing `v1.0.0` install), resolves digest-pinned `ateworker-gvisor`, patches `atenet-router` (`spiffe://cluster.local/ns/workshop-301/sa/sre-gateway`), and pre-pulls `ateworker-gvisor` onto all `Ready` nodes. |
   | 6. Local CLI tools | `make tools` | Compiles `bin/ledger-verify` and `bin/evalrunner`. |
   | 7. Incident simulation | `scripts/prod-sim.sh` (`make prod-sim`) | Builds and deploys live degraded `ledger-api` and `ops-hub` into namespace `prod-sim`. |
   | 8. Broker image pre-build | `make build-gateway` | Pre-builds and caches the `sre-gateway` digest in `.workshop/gateway-image` so `make gateway` deploys in seconds in Module 01. |
   | 9. Verification | `scripts/verify/00.sh` (`make verify MODULE=0`) | Confirms bucket, Artifact Registry, `ate-system`, `ComputeClass`, `ops-hub` (`INC-402` + `INC-409`), degraded `ledger-api`, `bin/ledger-verify`, and `bin/kubectl-ate`. |

3. **Load `bin/` and `workshop.env` into your current shell.** Because `make setup` runs in a subshell, load `bin/` (`kubectl-ate`, `ko`, `ledger-verify`, `evalrunner`) into your interactive shell `PATH` (and persist it to `~/.bashrc` so Cloud Shell reconnects keep `bin/` on `PATH`) before starting Module 01:
   ```bash
   export PATH="$PWD/bin:$PATH"
   grep -Fq "$PWD/bin" ~/.bashrc 2>/dev/null || echo "export PATH=\"$PWD/bin:\$PATH\"" >> ~/.bashrc
   source scripts/env.sh
   kubectl ate --help
   ```

4. **Inspect the live degraded `prod-sim` incident environment.** Port-forward both services and inspect the live incident and degraded SLO state:
   ```bash
   make forward-ops-hub
   make forward-ledger
   curl -s http://localhost:8082/api/incidents | jq .
   curl -s http://localhost:8083/api/status | jq .
   make stop-forwards
   ```
   **What this output means**: `ops-hub` serves `INC-402` (`OPEN`, connection-pool exhaustion) alongside `INC-403` and `INC-409`, and `ledger-api` reports `"status": "degraded"`, `"pool_exhausted": true`, and `"active_leaked_workers": 2`.

## Alternative: Run setup stages individually

If you prefer to step through each resource manually (or inspect the exact `gcloud` cluster command before running it), execute the individual stages below instead of `make setup`:

1. **Install `ko` and configure `workshop.env`:**
   ```bash
   go install github.com/google/ko@v0.19.1
   export PATH="$(go env GOPATH)/bin:$PATH"
   grep -Fq 'go env GOPATH' ~/.bashrc 2>/dev/null || echo 'export PATH="$(go env GOPATH)/bin:$PATH"' >> ~/.bashrc
   cp workshop.env.example workshop.env
   ${EDITOR:-nano} workshop.env
   ```
   Set `PROJECT_ID` and `CLUSTER_LOCATION` (`NAME` and `LOCATION` from `gcloud container clusters list`). Notice that `NAMESPACE` and `ATESPACE` default to `workshop-301` so this workshop can run side-by-side on a cluster that already hosts the intro workshop (`workshop`).

2. **Create the GKE `1.37+` cluster (skip if pre-provisioned).** Run `make create-cluster` (`scripts/create-cluster.sh`), or execute the underlying `gcloud` commands directly:
   ```bash
   export PROJECT_ID="YOUR_PROJECT_ID"
   export CLUSTER_LOCATION="us-central1-a"
   export CLUSTER_NAME="ate-cluster"

   gcloud services enable --project "$PROJECT_ID" \
     serviceusage.googleapis.com \
     cloudresourcemanager.googleapis.com \
     compute.googleapis.com \
     container.googleapis.com \
     monitoring.googleapis.com \
     logging.googleapis.com \
     cloudtrace.googleapis.com \
     telemetry.googleapis.com

   gcloud beta container clusters create "$CLUSTER_NAME" \
     --project "$PROJECT_ID" \
     --location "$CLUSTER_LOCATION" \
     --release-channel rapid \
     --cluster-version 1.37 \
     --machine-type c3-standard-4 \
     --num-nodes 2 \
     --disk-type pd-balanced \
     --disk-size 100 \
     --scopes=https://www.googleapis.com/auth/cloud-platform \
     --gateway-api=standard \
     --workload-pool="${PROJECT_ID}.svc.id.goog" \
     --enable-managed-prometheus \
     --managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS

   FREEZE_START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
   FREEZE_END="$(date -u -d '+2 days' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
     || date -u -v+2d +%Y-%m-%dT%H:%M:%SZ)"

   gcloud container clusters update "$CLUSTER_NAME" \
     --project "$PROJECT_ID" \
     --location "$CLUSTER_LOCATION" \
     --add-maintenance-exclusion-name workshop-freeze \
     --add-maintenance-exclusion-start "$FREEZE_START" \
     --add-maintenance-exclusion-end "$FREEZE_END" \
     --add-maintenance-exclusion-scope no_minor_or_node_upgrades

   gcloud container clusters get-credentials "$CLUSTER_NAME" \
     --location "$CLUSTER_LOCATION" \
     --project "$PROJECT_ID"
   ```

3. **Run preflight, GCP bootstrap, tools, Agent Substrate + `prod-sim`, and `sre-gateway` pre-build:**
   ```bash
   make preflight
   make gcp
   export PATH="$PWD/bin:$PATH"
   grep -Fq "$PWD/bin" ~/.bashrc 2>/dev/null || echo "export PATH=\"$PWD/bin:\$PATH\"" >> ~/.bashrc
   source scripts/env.sh
   make tools
   make install-bg
   make install-status
   ```
   Wait until `make install-status` reports `install finished` (typically 3–5 minutes with published images; `make install-bg` installs Agent Substrate and deploys `prod-sim` together), then pre-build the `sre-gateway` image:
   ```bash
   make build-gateway
   ```

## Checkpoint

Verify all Module 00 prerequisites, GCP resources, Agent Substrate installation, `prod-sim` deployments, and shell `PATH`:

```bash
make verify MODULE=0
```

**Need to catch up?** Run `make catch-up MODULE=0` (or `make setup` to also create the cluster and pre-pull `ateworker-gvisor`), and see `instructor/troubleshooting.md` for common diagnostics.

Next: [Actor boundary and first triage turn](../01-actor-boundary/README.md)
