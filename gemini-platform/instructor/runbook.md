# Instructor runbook

**Validated target:** `SUBSTRATE_REF=v1.0.0` (`SUBSTRATE_IMAGE_REPO=ghcr.io/agent-substrate/substrate`, `SUBSTRATE_IMAGE_TAG=${SUBSTRATE_REF}`). Live-validated end-to-end across Modules `00` through `07` on GKE `1.37` Rapid (`2× c3-standard-4` BYO and `2× n1-standard-4` Intel Skylake classroom sizing) against `gemini-3.8-flash` (`MOCK_LLM=false`, `MODEL_LOCATION=global`).

**Measured live benchmarks (published images):**
- **Cold Module 00 setup (`make gcp` + `make install-bg` + `make prod-sim` + `make verify MODULE=0`)**: **`2m 45s`** end-to-end.
- **Incremental `ko` agent build (`make build-agent` on `alpine/git:latest`)**: **under 5 s**.
- **`ActorTemplate` Golden Snapshot creation (`sre-agent-v1..v4`)**: **`15–25s`**.
- **Suspended Incident Actor wake latency (`ResumeDurationMs` via `atenet-router` from Cloud Storage `SNAPSHOT_FIDELITY_MEMORY`)**: **under 100 ms** (classroom SLO: under 400 ms).

## One week before

1. **Validate `SUBSTRATE_REF` end-to-end on a scratch Google Cloud project.**
   - **Published image mode (default in `workshop.env.example`)**: `SUBSTRATE_IMAGE_REPO=ghcr.io/agent-substrate/substrate` and `SUBSTRATE_IMAGE_TAG=${SUBSTRATE_REF}` deploy `ate-system` and pin `ateworker-gvisor` without local Docker builds, keeping Cloud Shell home-directory usage well inside its `5 GB` limit.
   - **Source build mode**: clear `SUBSTRATE_IMAGE_REPO` and `SUBSTRATE_IMAGE_TAG` in `workshop.env` only when testing upstream Substrate commits with a local Docker `buildx` daemon.
   - Run the full cluster contract suite with `bin/` on `PATH`:
     ```bash
     export PATH="$PWD/bin:$PATH"
     make e2e
     ```
2. **Verify the deterministic fallback path (`MOCK_LLM=true`).**
   - Confirm the workshop still passes end-to-end if venue Wi-Fi or regional model quota degrades:
     ```bash
     MOCK_LLM=true make e2e
     MOCK_LLM=false make gateway
     ```
3. **Verify external pins that drift independently of Agent Substrate.**
   - Confirm `GEMINI_MODEL=gemini-3.8-flash` is active at `MODEL_LOCATION=global` on **Gemini platform**.
   - Confirm `github.com/google/ko@v0.19.1` builds cleanly on the current Cloud Shell Go version.
   - On Linux workstations with context-aware client certificates enabled in `gcloud`, set `export CLOUDSDK_CORE_DISABLE_USAGE_REPORTING=true` (already exported inside `scripts/lib.sh`) so background telemetry subprocesses do not race on `~/.config/gcloud/caa_cert.pem`.
4. **Provision attendee projects and GKE `1.37+` clusters.**
   - **Bring-your-own / scratch projects (`2× c3-standard-4`)**:
     ```bash
     gcloud beta container clusters create "$CLUSTER_NAME" \
       --project "$PROJECT_ID" \
       --location us-central1-a \
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
       --location us-central1-a \
       --add-maintenance-exclusion-name workshop-freeze \
       --add-maintenance-exclusion-start "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
       --add-maintenance-exclusion-end "$(date -u -d '+2 days' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+2d +%Y-%m-%dT%H:%M:%SZ)" \
       --add-maintenance-exclusion-scope no_minor_or_node_upgrades
     ```
   - **Google Skills (Qwiklabs) classrooms (`gcp_medium_extra`)**:
     - **Resource budget**: `gcp_medium_extra` (`duration: 90`, `max_duration: 180`) enforces an `8 vCPU` and `700 GB` Persistent Disk SSD cap per student project. Provision a zonal GKE `1.37` Rapid cluster (`ate-cluster`) with **`2× n1-standard-4`** nodes pinned to `--min-cpu-platform="Intel Skylake"` so `make preflight` passes uniform-CPU checks and cross-worker `SNAPSHOT_FIDELITY_MEMORY` restores succeed.
     - **Zero-autoscale pod fit**: Total workshop pod requests (`2× 250m / 1Gi` `WorkerPool`, `100m / 128Mi` `sre-gateway`, `50m / 64Mi` `ledger-api`, and `50m / 64Mi` `ops-hub` = `700m` CPU / `2.25Gi` RAM) schedule onto the two `n1-standard-4` nodes with `ENABLE_COMPUTE_CLASS_AUTOPROVISION=false`, and Substrate's `500 GiB` bundled Postgres PVC fits within the `700 GB` SSD quota.
     - **HotLab pre-warming (`max_hot_labs`)**: `gcp_medium_extra` forbids system-wide HotLabs (`PrivilegedProjectVariantPolicy`). Configure **per-classroom HotLabs** (`max_hot_labs: 150` or room headcount + 20%) on a trusted classroom (in-person event or domain-restricted virtual classroom with no `*` wildcard student filter). Optionally run `make gcp && make install && make prod-sim` in the lab Terraform startup script so students begin with `ate-system` and `prod-sim` already warm.

## Day of

- Keep a dedicated instructor cluster caught up to the completed state (`make catch-up MODULE=7` and `make forward-gateway`) so you can demonstrate `/`, `/sessions`, `/catalog`, and `/about` live if an attendee hits a transient quota delay.
- Test `make forward-gateway` (`localhost:8081`) or Cloud Shell **Web Preview (port 8081)** over the room's conference Wi-Fi before doors open.
- Keep [`instructor/troubleshooting.md`](troubleshooting.md) open in a browser tab.

## 90-minute standard timing and talk track

| Module | Min | Core teaching point | Cut if behind |
|---|---|---|---|
| 0 Setup | 10 | Uniform-CPU memory restore requirement, quota-safe `ComputeClass`, async runtime bootstrap (`2m 45s` cold install). | Skip local `forward-ops-hub` / `forward-ledger` curls; jump straight to `make verify MODULE=0`. |
| 1 Actor boundary | 12 | Stateful `/workspace` git tools inside gVisor vs. zero ambient credentials; `sre-gateway` holds identity. | Paste `WORKSHOP(module-1)` from `modules/01-actor-boundary/solution/main.go`. |
| 2 Sessions & memory | 15 | Raw GCS snapshot (`1` actor, `1` template) vs. `0 vCPU` **Gemini Sessions** ledger vs. cross-actor **Gemini Memory Bank**. | Demo `INC-403` sibling recall on screen and move to `make verify MODULE=2`. |
| 3 Skill registry | 12 | Baking `SKILL.md` into images churns Golden Snapshots; `SkillToolset` pulls SHA-256 `v1 -> v2` archives live. | Inspect `v1` vs. `v2` diffs together instead of editing `SKILL.md` manually. |
| 4 Agent registry, MCP & A2A | 14 | GKE Agent Card discovery, governed MCP toolsets, and `toolconfirmation` parking at `0 vCPU` (under 100 ms wake). | Demo step 6 (`POST /a2a` peer call) from the instructor terminal. |
| 5 Zero-trust governance | 14 | Zero agent rebuilds: `GCPAgentGateway` + `GCPAuthzPolicy`, Pod **Agent Identity**, **Model Armor** `403` on `INC-409`, 3LO PKCE, `0`-token snapshots. | Use headless `complete_3lo_pkce_grant` if browser popup blockers slow down OAuth consent. |
| 6 Observability & evals | 10 | `ActorTemplate`-scoped Prometheus metrics vs. scrubbed W3C `gen_ai.*` traces + pure-Go `make eval` + SCC Anomaly Detection. | Inspect the **Incident Console** **Trace & online evals** waterfall instead of waiting for Cloud Monitoring chart lag. |
| 7 Wrap-up | 3 | Inside vs. outside the **Actor Boundary** summary, optional Ambient + SWP egress overlay, and teardown. | Point self-paced readers to the Appendix for `ambient-swp-egress-overlay.yaml.tmpl`. |

If an attendee falls more than one module behind, have them run `make catch-up MODULE=N` (adding `RESET=1` if they built an incomplete `sre-agent-v*` template) to land cleanly at the start of module `N`.

## 60-minute compressed session cut line

When delivering this workshop in a 60-minute conference breakout slot:

1. **Pre-warm Module 00 (`0 min` in-room)**: Run `make catch-up MODULE=1` during classroom provisioning so `ate-system`, `prod-sim`, `ComputeClass/substrate-gvisor`, and the regional `Gateway` are already running when attendees open Cloud Shell.
2. **Modules 01–02 hands-on (`25 min`)**: Attendees deploy `WorkerPool/sre-workers` and `sre-gateway`, complete `WORKSHOP(module-1)` and `WORKSHOP(module-2)`, inspect `inc-402` at `0 vCPU` in `/sessions`, and test `INC-403` memory recall.
3. **Module 03 live demo + `catch-up MODULE=5` (`10 min`)**: Demonstrate `make publish-skill REV=v1` -> `REV=v2` hot-reloading on the instructor screen (`SNAP_BEFORE == SNAP_AFTER`), then have attendees run `make catch-up MODULE=5` to stage `sre-agent-v4`.
4. **Module 04 Remediation Gate (`10 min`)**: Attendees trigger `INC-402` remediation, verify `inc-402` suspends to `0 vCPU` at `adk_request_confirmation`, and click **Approve remediation** in the **Incident Console** to watch under-100ms wake and `ledger-api` recovery.
5. **Modules 05–06 governance & evals (`15 min`)**: Neither module rebuilds `cmd/agent`. Attendees run `make governance`, trigger the `INC-409` **Model Armor** `403` block, complete 3LO authorization, run `make audit-snapshot` (`0` token matches), and run `make eval` (`>= 0.80` across all 4 rubrics).

## After the event

- **Keep cluster, reset workshop state**: `make teardown YES=1` deletes `ATESPACE` (`workshop-301`), namespaces `workshop-301` and `prod-sim`, `ComputeClass/substrate-gvisor`, `PodMonitoring/sre-gateway`, and Cloud Storage actor snapshots while preserving `ate-system`.
- **Full resource cleanup**: `make teardown ALL=1 YES=1` additionally deletes the snapshot bucket, Artifact Registry repository, `ReasoningEngine` runtime, **Model Armor** template, **AuthProvider**, **Skill Registry** bundle, project IAM bindings, and `ate-snapshot-transfer` service account. Note that `ate-system` cannot snapshot or pull workshop images afterward until you re-run `make gcp && make install`.
- **Cluster deletion**: `make delete-cluster YES=1` scales down `ate-system/postgres`, deletes all PersistentVolumeClaims so underlying disks do not outlive the cluster, and deletes `CLUSTER_NAME`.
