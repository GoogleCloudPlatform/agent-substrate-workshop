# Setup (10 min)

In this section, you point the workshop at your Google Cloud project, validate that your GKE **1.37+** cluster meets cross-worker memory snapshot requirements, provision cloud storage and **Gemini Enterprise Agent Platform** prerequisites, apply a quota-safe `ComputeClass`, start **Agent Substrate (`v1.0.0`)** installation in the background, and deploy the live degraded `prod-sim` incident environment.

If you create a new GKE cluster in Step 3 rather than using a pre-provisioned classroom cluster, allow ~20 minutes total.

## What you need

- A Google Cloud project where you hold **Owner** permissions (or a pre-provisioned Google Skills classroom project) and a GKE **1.37+** cluster with:
  - Workload Identity Federation for GKE (`${PROJECT_ID}.svc.id.goog`)
  - Managed Service for Prometheus and managed OpenTelemetry collection
  - Disabled node auto-upgrade (or an active `no_minor_or_node_upgrades` maintenance exclusion) and standard non-spot nodes
  - **Uniform-CPU node pools** (`c3-standard-4`, `n4-standard-4`, CPU-pinned `n2-standard-4`, or Skylake-pinned `n1-standard-4`) so `SNAPSHOT_FIDELITY_MEMORY` checkpoints restore cleanly across any worker node
- Local tools: `gcloud`, `kubectl`, `go` 1.26+ (with `GOTOOLCHAIN=auto` automatically downloading `go1.27` from `app/go.mod`, or `go` 1.27+), `ko`, `jq`, `envsubst` (gettext), `curl`, `git`, and `python3`. Cloud Shell includes all of these except `ko`.

## Architecture context

In **Agent Substrate: Gemini Enterprise Agent Platform**, every production incident gets its own stateful **Incident Actor** inside an `ateworker-gvisor` sandbox (`workshop-301`), separated across the **Actor Boundary** from an always-on **Governed Broker** (`sre-gateway`) and **Gemini Enterprise Agent Platform** services.

To eliminate cloud provisioning waits later in the workshop, `make gcp` immediately starts two long-running control-plane reconciliations in the background while Substrate installs:

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

## Steps

1. **Activate Cloud Shell and install `ko`.** Open the Google Cloud console, click **Activate Cloud Shell**, install `ko`, and add `$(go env GOPATH)/bin` to your `PATH`:
   ```bash
   go install github.com/google/ko@v0.19.1
   export PATH="$(go env GOPATH)/bin:$PATH"
   echo 'export PATH="$(go env GOPATH)/bin:$PATH"' >> ~/.bashrc
   ```

2. **Clone the repository and enter the workshop directory.**
   ```bash
   git clone https://github.com/GoogleCloudPlatform/agent-substrate-workshop
   cd agent-substrate-workshop/gemini-platform
   ```

3. **Create a GKE 1.37+ cluster (skip if pre-provisioned).** Check whether an instructor or Google Skills classroom already created a cluster (`gcloud container clusters list`). If none exists, create a two-node `c3-standard-4` cluster with 100 GB `pd-balanced` boot disks and freeze node upgrades for 48 hours:
   ```bash
   export PROJECT_ID="YOUR_PROJECT_ID"
   export CLUSTER_LOCATION="us-central1-a"
   export CLUSTER_NAME="ate-cluster"

   gcloud services enable \
     container.googleapis.com \
     monitoring.googleapis.com \
     logging.googleapis.com \
     cloudtrace.googleapis.com \
     --project "$PROJECT_ID"
   gcloud beta container clusters create "$CLUSTER_NAME" \
     --project "$PROJECT_ID" \
     --location "$CLUSTER_LOCATION" \
     --release-channel rapid \
     --cluster-version 1.37 \
     --machine-type c3-standard-4 \
     --disk-type pd-balanced \
     --disk-size 100 \
     --num-nodes 2 \
     --gateway-api=standard \
     --workload-pool "$PROJECT_ID.svc.id.goog" \
     --enable-managed-prometheus \
     --managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS

   gcloud container clusters update "$CLUSTER_NAME" \
     --project "$PROJECT_ID" \
     --location "$CLUSTER_LOCATION" \
     --add-maintenance-exclusion-name workshop-freeze \
     --add-maintenance-exclusion-start "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
     --add-maintenance-exclusion-end "$(date -u -d '+2 days' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+2d +%Y-%m-%dT%H:%M:%SZ)" \
     --add-maintenance-exclusion-scope no_minor_or_node_upgrades
   ```
   If your project doesn't have `C3_CPUS` quota for `c3-standard-4`, use `--machine-type n2-standard-4 --min-cpu-platform="Intel Ice Lake"` (or `--machine-type n1-standard-4 --min-cpu-platform="Intel Skylake"`) instead. Do **not** use `e2-standard-4`, which mixes CPU platforms across nodes and breaks cross-worker `MEMORY` snapshot restores.

4. **Configure `workshop.env` and cluster credentials.** Copy `workshop.env.example`, set `PROJECT_ID`, `CLUSTER_LOCATION` (`NAME` and `LOCATION` from `gcloud container clusters list`), and `CLUSTER_NAME`, and fetch `kubectl` credentials for the cluster. Notice that `NAMESPACE` and `ATESPACE` default to `workshop-301` so Track 301 can run side-by-side on a cluster that already hosts Track 101 (`workshop`).
   ```bash
   # If you skipped Step 3, set these to match `gcloud container clusters list`:
   export PROJECT_ID="${PROJECT_ID:-YOUR_PROJECT_ID}"
   export CLUSTER_LOCATION="${CLUSTER_LOCATION:-us-central1-a}"
   export CLUSTER_NAME="${CLUSTER_NAME:-ate-cluster}"

   cp workshop.env.example workshop.env
   sed -i \
     -e "s|^PROJECT_ID=.*|PROJECT_ID=${PROJECT_ID}|" \
     -e "s|^CLUSTER_NAME=.*|CLUSTER_NAME=${CLUSTER_NAME}|" \
     -e "s|^CLUSTER_LOCATION=.*|CLUSTER_LOCATION=${CLUSTER_LOCATION}|" \
     workshop.env
   gcloud container clusters get-credentials "$CLUSTER_NAME" \
     --location "$CLUSTER_LOCATION" \
     --project "$PROJECT_ID"
   ```

5. **Run preflight checks.** Validate your local tools, cluster credentials, GKE `1.37+` control plane, observability add-ons, and uniform-CPU node pools (`e2-*` pools fail preflight because mixed Intel/AMD hosts cannot restore `MEMORY`-fidelity gVisor snapshots across workers):
   ```bash
   make preflight
   ```

6. **Bootstrap Google Cloud resources and async runtimes.** Enable required APIs, create the snapshot Cloud Storage bucket and Artifact Registry repository, bind least-privilege Workload Identity roles for Substrate and `sre-gateway`, apply `ComputeClass/substrate-gvisor`, and start regional `Gateway`/`GCPAgentGateway` + session/memory runtime provisioning in the background:
   ```bash
   make gcp
   ```
   Inspect `modules/00-setup/manifests/computeclass.yaml.tmpl`: it defines a compute obtainability priority chain across uniform-CPU families (`c3` -> `n4` -> `n2`), sets `consolidationDelayMinutes: 30` so scaled-up worker nodes retain local Golden Snapshot caches, and raises `inotify` and ARP neighbor table sysctls for high sandbox density, while keeping `nodePoolAutoCreation.enabled: false` by default (`ENABLE_COMPUTE_CLASS_AUTOPROVISION=true` opt-in) to respect fixed classroom vCPU/SSD quotas.

7. **Add `bin/` to your shell `PATH` and build local verification tools.**
   ```bash
   export PATH="$PWD/bin:$PATH"
   echo 'export PATH="'"$PWD"'/bin:$PATH"' >> ~/.bashrc
   source scripts/env.sh
   make tools
   ```

8. **Start Agent Substrate `v1.0.0` installation and `prod-sim` bootstrap in the background.** This clones Substrate at `v1.0.0`, compiles `bin/kubectl-ate`, deploys `ate-system` with published `ghcr.io/agent-substrate/substrate` images, pins the `ateworker-gvisor` digest, idempotently appends `spiffe://cluster.local/ns/workshop-301/sa/sre-gateway` to `atenet-router`, and deploys `prod-sim` (`ledger-api` + `ops-hub`) asynchronously at the tail of setup:
   ```bash
   make install-bg
   make install-status
   ```

9. **Deploy (or confirm) and inspect the live `prod-sim` incident environment.** While Substrate finishes installing in the background, you can run standalone `make prod-sim` idempotently right away (or let `make install-bg` deploy it automatically):
   ```bash
   make prod-sim
   ```
   Port-forward both services and inspect the live incident and degraded SLO state:
   ```bash
   make forward-ops-hub
   make forward-ledger
   curl -s http://localhost:8082/api/incidents | jq .
   curl -s http://localhost:8083/api/status | jq .
   make stop-forwards
   ```
   You should see `INC-402` (`OPEN`, connection-pool exhaustion) alongside `INC-409`, and `ledger-api` reporting `"status": "degraded"`, `"pool_exhausted": true`, and `"active_leaked_workers": 2`.

## Checkpoint

Wait until `make install-status` reports `install finished` (typically 3–5 minutes with published images), then verify Module 00:

```bash
make verify MODULE=0
```

**Need to catch up?** Run `make catch-up MODULE=0` (or `make catch-up MODULE=1`) to execute all Module 00 steps idempotently in the foreground, and see `instructor/troubleshooting.md` for common diagnostics.

Next: [Actor boundary and first triage turn](../01-actor-boundary/README.md)
