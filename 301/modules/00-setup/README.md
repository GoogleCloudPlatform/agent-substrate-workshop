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

In **Agent Substrate 301**, every production incident gets its own stateful **Incident Actor** inside an `ateworker-gvisor` sandbox (`workshop-301`), separated across the **Actor Boundary** from an always-on **Governed Broker** (`sre-gateway`) and **Gemini Enterprise Agent Platform** services.

To eliminate cloud provisioning waits later in the workshop, `make gcp` immediately starts two long-running control-plane reconciliations in the background while Substrate installs:

1. **Regional Ingress & Agent Gateways** (`Gateway/sre-ingress-gateway` on `gke-l7-regional-external-managed` plus internal `Gateway/sre-agent-gateway` on `gke-l7-rilb` targeted by `GCPAgentGateway/sre-client-to-agent-gateway` in [`ingress-gateway.yaml.tmpl`](manifests/ingress-gateway.yaml.tmpl)), giving the regional load balancers time to allocate addresses before Module 05 attaches zero-trust policies.
2. **Managed Session & Memory Runtime** (`ReasoningEngine`), backing **Gemini Sessions** and **Gemini Memory Bank** in Module 02.

At the same time, `make prod-sim` deploys two lightweight workloads (`50m` CPU / `64Mi` RAM each) into namespace `prod-sim`:

- **`ledger-api` (Target Service)**: A real Go transfer reconciliation service configured with `MAX_OPEN_CONNS=2`, `IDLE_TIMEOUT_SECONDS=0`, and an unclosed row-cursor bug on checksum errors. Within seconds of boot, synthetic reconciliation batches exhaust its connection pool, leak parked worker goroutines, fail `/healthz` with HTTP `503`, and emit structured `severity=ERROR` (`pool_exhausted`) JSON logs to Cloud Logging.
- **`ops-hub` (Operations Hub)**: A self-contained Smart HTTP Git server (`/git/ledger-api.git`), incident tracker (`INC-402` clean P1 pool exhaustion; `INC-409` poisoned partner note containing an indirect prompt-injection payload), governed remediation MCP server (`/mcp`), and OAuth 2.0 Authorization Code + S256 PKCE provider (`/oauth/*`) so the entire curriculum runs with zero external SaaS account signups.

## Steps

1. **Activate Cloud Shell and install `ko`.** Open the Google Cloud console, click **Activate Cloud Shell**, and install `ko`:
   ```bash
   go install github.com/google/ko@v0.19.1
   export PATH="$(go env GOPATH)/bin:$PATH"
   ```

2. **Clone the repository.** Clone the repository and move into `agent-substrate-workshop/301`:
   ```bash
   git clone https://github.com/GoogleCloudPlatform/agent-substrate-workshop
   cd agent-substrate-workshop/301
   ```

3. **Create a GKE 1.37+ cluster (skip if pre-provisioned).** Check whether an instructor or Google Skills classroom already created a cluster (`gcloud container clusters list`). If none exists, create a two-node `c3-standard-4` cluster and freeze node upgrades for 48 hours:
   ```bash
   export PROJECT_ID="YOUR_PROJECT_ID"
   export CLUSTER_LOCATION="us-central1-a"
   export CLUSTER_NAME="ate-cluster"

   gcloud services enable container.googleapis.com --project "$PROJECT_ID"
   gcloud beta container clusters create "$CLUSTER_NAME" \
     --project "$PROJECT_ID" \
     --location "$CLUSTER_LOCATION" \
     --release-channel rapid \
     --cluster-version 1.37 \
     --machine-type c3-standard-4 \
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

4. **Configure `workshop.env`.** Copy `workshop.env.example` and set `PROJECT_ID` and `CLUSTER_LOCATION` (`NAME` and `LOCATION` from `gcloud container clusters list`). Notice that `NAMESPACE` and `ATESPACE` default to `workshop-301` so Track 301 can run side-by-side on a cluster that already hosts Track 101 (`workshop`).
   ```bash
   gcloud container clusters list --project YOUR_PROJECT_ID
   cp workshop.env.example workshop.env
   ${EDITOR:-nano} workshop.env
   ```

5. **Run preflight checks.** Validate your local tools, cluster credentials, GKE `1.37+` control plane, observability add-ons, and uniform-CPU node pools (`e2-*` pools fail preflight because mixed Intel/AMD hosts cannot restore `MEMORY`-fidelity gVisor snapshots across workers):
   ```bash
   make preflight
   ```

6. **Bootstrap Google Cloud resources and async runtimes.** Enable required APIs, create the snapshot Cloud Storage bucket and Artifact Registry repository, bind least-privilege Workload Identity roles for Substrate and `sre-gateway`, apply `ComputeClass/substrate-gvisor`, and start regional `Gateway`/`GCPAgentGateway` + session/memory runtime provisioning in the background:
   ```bash
   make gcp
   ```
   Inspect [`modules/00-setup/manifests/computeclass.yaml.tmpl`](manifests/computeclass.yaml.tmpl): it defines a compute obtainability priority chain across uniform-CPU families (`c3` -> `n4` -> `n2`), sets `consolidationDelayMinutes: 30` so scaled-up worker nodes retain local Golden Snapshot caches, and raises `inotify` and ARP neighbor table sysctls for high sandbox density, while keeping `nodePoolAutoCreation.enabled: false` by default (`ENABLE_COMPUTE_CLASS_AUTOPROVISION=true` opt-in) to respect fixed classroom vCPU/SSD quotas.

7. **Add `bin/` to your shell `PATH` and build local verification tools.**
   ```bash
   export PATH="$PWD/bin:$PATH"
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

**Need to catch up?** Run `make catch-up MODULE=0` (or `make catch-up MODULE=1`) to execute all Module 00 steps idempotently in the foreground, and see [troubleshooting](../../instructor/troubleshooting.md).

Next: [Actor boundary and first triage turn](../01-actor-boundary/README.md)
