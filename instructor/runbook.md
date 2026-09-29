# Instructor runbook

**Last validated:** 2026-09-27, `SUBSTRATE_REF=v0.2.0`, GKE `1.37.0-gke.3503000` (Rapid, zonal, 2× `c3-standard-4`),
source install mode. `make e2e` passed with real Vertex (14.5 min on a fresh cluster, install included) and with
`MOCK_LLM=true` (7 min, install already done). Optional lab B passed by hand. Not run: published-image mode
(the v0.2.0 release notes name no image repo), and the other optional labs. Lab A's `ENV_REF` patch was checked at the
compile/unit-test level only. Lab G's agent-swarm commit (`7f22bb5`) pins v0.2.0, which matches the node label
`make install` writes, but it hasn't been deployed on a workshop cluster.

## One week before

1. **Pick `SUBSTRATE_REF`**: the newest tag or commit that passes `make e2e` in **both** install modes,
   against a scratch GCP project (not an attendee's).
   - Source mode: leave `SUBSTRATE_IMAGE_REPO`/`SUBSTRATE_IMAGE_TAG` empty in `workshop.env` — `install-substrate.sh`
     clones `SUBSTRATE_REF` and ko-builds `ate-setup`, `kubectl-ate`, and `ateom-gvisor` from source.
   - Published mode: set both `SUBSTRATE_IMAGE_REPO` and `SUBSTRATE_IMAGE_TAG` if images exist for that ref, so
     `install-substrate.sh` runs `ate-setup deploy ate-system --image-repo … --image-tag …` instead of building.
   - `make e2e` drives every module through `catch-up` + `verify` (`scripts/catch-up.sh $n && scripts/verify/$((n-1)).sh`
     for n = 1..7). Run it start to finish in **each** mode; a pass in one mode does not imply a pass in the other,
     since the worker image (`ateom-gvisor`) comes from a different path in each. Run it with `bin/` on your
     PATH (`export PATH="$PWD/bin:$PATH"`, as attendees do in Module 0), or `verify/00` fails its PATH check.
   - Put the validated ref in `workshop.env.example`'s `SUBSTRATE_REF` and in `app/go.mod`
     (`cd app && go get github.com/agent-substrate/substrate@<ref> && go mod tidy`), since `ateapipb` (used by the
     gateway, `egressctl`, and the idle-suspender) must speak the same protocol as the control plane you install.
2. **Run the mock path too**: `MOCK_LLM=true make e2e`, so you know the whole workshop still works when Vertex is
   unreachable (quota, a locked-down project, a flaky network) — the fallback attendees are told about in Module 4.
   The gateway keeps that live `MOCK_LLM=true` afterwards; run `MOCK_LLM=false make gateway` to go back to Vertex.
3. **Check external dependencies that drift independently of substrate:**
   - `GEMINI_MODEL` (`workshop.env.example`, currently `gemini-2.5-flash`) is still served in `VERTEX_LOCATION`
     (currently `${REGION}`, i.e. `us-central1`).
   - `PROM_FRONTEND_IMAGE` (currently `gke.gcr.io/prometheus-engine/frontend:v0.15.3-gke.0`) is a current tag.
4. **Re-check [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) releases.** If it has
   gained auto suspend/resume or multi-sandbox-per-Pod, update the comparison table in
   [Module 1](../modules/01-architecture/README.md) and the wrap-up table in
   [Module 7](../modules/07-wrap-up/README.md).
5. **For optional lab A** (`agent-substrate/env`): pick a tag whose `go.mod` substrate version matches
   `SUBSTRATE_REF`, so the sandboxed-shell lab isn't running against a different substrate build than the core
   workshop.
   **For optional lab G** (`moficodes/agent-swarm`, which must be public before the event): pick a commit whose `go.mod` substrate version is
   exactly `SUBSTRATE_REF`. Its `hack/deploy.sh` refuses to deploy onto a cluster running any other version.
6. **Provision attendee projects and clusters.** Each attendee is Owner on their own GCP project, with a
   bare GKE cluster that has, at creation:
   - GKE **1.37+** (the minimum the workshop is validated on; `make preflight` rejects older) with `--enable-kubernetes-unstable-apis=certificates.k8s.io/v1beta1/clustertrustbundles,certificates.k8s.io/v1beta1/podcertificaterequests`.
     Substrate at `SUBSTRATE_REF` still reads ClusterTrustBundles only at `v1beta1` (PodCertificateRequest works at
     either version since v0.2.0, but substrate's own GCP setup still enables both, so we do too). 1.37 serves them at `v1` and
     doesn't serve `v1beta1` unless you enable it; without it, `make install` hangs waiting for the
     `primary-bundle` ClusterTrustBundle. `make preflight` asks the API server for `v1beta1` directly.
   - **Workload Identity** and **managed OpenTelemetry** with `--managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS`.
   - **Managed Prometheus** enabled (`--enable-managed-prometheus`) — Module 6's Prometheus UI reads through it.
   - Machine type **`c3-standard-4`**, 2 nodes, **no node auto-upgrade**, **no spot/preemptible nodes** (an
     upgrade or a preempted node can silently swap in a worker image `ate.dev/substrate-version` doesn't match,
     and a spot reclaim mid-workshop loses whatever wasn't already snapshotted).

   `--managed-otel-scope` is a **beta** flag: plain `gcloud container clusters create` rejects it
   (`unrecognized arguments`). Use `gcloud beta` (`gcloud components install beta` first).

   GKE no longer lets new customers create clusters with `--release-channel None` (the create fails with
   `not enrolling clusters in a release channel is now only allowed for existing customers`), and a release
   channel forces node auto-upgrade on. So pick the channel that serves 1.37 (`gcloud container get-server-config
   --location us-central1-a --format="yaml(channels)"`; Rapid at the time of writing), then freeze upgrades with a
   maintenance exclusion. `clusters create` has no exclusion flags, so it is a second command. `make preflight`
   accepts an active `no_minor_or_node_upgrades` exclusion in place of `--no-enable-autoupgrade`. A zonal
   cluster matching the list above:
   ```bash
   gcloud beta container clusters create "$CLUSTER_NAME" --project "$PROJECT_ID" --location us-central1-a \
     --release-channel rapid --cluster-version 1.37 \
     --machine-type c3-standard-4 --num-nodes 2 \
     --workload-pool "$PROJECT_ID.svc.id.goog" --enable-managed-prometheus \
     --managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS \
     --enable-kubernetes-unstable-apis=certificates.k8s.io/v1beta1/clustertrustbundles,certificates.k8s.io/v1beta1/podcertificaterequests
   # Start must be in the past (or now) for preflight to count it; cover the workshop day with the end.
   gcloud container clusters update "$CLUSTER_NAME" --project "$PROJECT_ID" --location us-central1-a \
     --add-maintenance-exclusion-name workshop-freeze \
     --add-maintenance-exclusion-start "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
     --add-maintenance-exclusion-end <YYYY-MM-DD>T00:00:00Z \
     --add-maintenance-exclusion-scope no_minor_or_node_upgrades
   ```
   Projects that still may create unenrolled clusters can use `--release-channel None --no-enable-autoupgrade`
   instead and skip the exclusion. Adding the unstable APIs to an existing cluster with
   `clusters update` works too, but takes 10+ minutes and needs `kubectl -n podcertificate-controller-system rollout restart deploy/podcertificate-controller`
   afterwards if substrate is already installed.
   With a regional `--location`, `--num-nodes` is per zone (2 × 3 zones = 6 nodes). Put the cluster's
   location in each attendee's `workshop.env` as `CLUSTER_LOCATION`.

   Send each attendee their `PROJECT_ID` and confirm they can `gcloud auth login` and reach it before the day.

## Day of

- Arrive with your own cluster already caught up: `make catch-up MODULE=7`. Use it to demo any step that fails
  for the room, and as a known-good reference if an attendee's environment diverges.
- Keep [`instructor/troubleshooting.md`](troubleshooting.md) open in a tab.
- Confirm `make forward-router`, `make forward-gateway`, and `make prometheus-ui` all work from the room's
  network (some corporate VPNs block the long-lived connections they depend on) — this is worth testing from
  a guest-network laptop, not just your own, before the event.

## Timing (85 min) and talk track

| Module | Min | Say | Cut if behind |
|---|---|---|---|
| 0 Setup | 10 | "The install runs in the background while we talk." | — |
| 1 Architecture | 10 | Agents idle most of the time. Actors vs Pods. CRDs vs records. | Skip "Look around" for everyone but you |
| 2 Hello actor | 15 | Golden snapshot = every actor's starting point. Create is cheap. | — |
| 3 Suspend/resume | 12 | FULL vs DATA. The router hides resumes. Templates are immutable. | Part B logs step |
| 4 Agent | 13 | No secrets in actors. Egress is deny-by-default. Presence vs cognition. | The `egress-open` comparison |
| 5 Density & idle | 10 | Substrate doesn't know when you're idle; your front door does. | Step 4 (the 60s 503 demo): describe it instead |
| 6 Observe | 12 | Platform metrics + product metrics. The 1–3 min lag. | Logs Explorer step |
| 7 Wrap-up | 3 | Point everyone at lab A. | — |

That's 85 minutes with no slack. If a module runs long, take the "cut if behind" for that module rather than
compressing the next one — Module 6 in particular needs its 1–3 minute Cloud Monitoring lag no matter how fast
you talk.

If someone falls more than one module behind, have them run `make catch-up MODULE=<current>` and keep going.
Every module README starts from the previous module's verified end state (`make verify MODULE=<current - 1>`),
which is exactly what `catch-up.sh <current>` produces.

## After

Ask attendees to run `make teardown` (keeps their cluster, bucket, and Artifact Registry — only the in-cluster
workshop state goes away), or `YES=1 scripts/teardown.sh --all` if the whole project is being torn down too
(also deletes the bucket and the Artifact Registry repo, which takes the grants on them along, and the
workshop namespace's project IAM bindings `make gcp` created). Artifact Registry read access is granted on the
workshop repo only, so no other cluster in the project loses image pulls. Neither
one touches the GKE cluster, and neither deletes Substrate's own `ate-system` Deployments/DaemonSets — but
**`--all` leaves the already-running Substrate unable to actually work**: in source install mode its
control-plane and worker images lived in the Artifact Registry repo that was just deleted, and the IAM grants
that let `atelet`/`ateapi` write snapshots are gone too. If the project is being kept around rather than
deleted outright, `--all` needs a full `make install` (or `make catch-up`) afterward before Substrate is usable
again.
