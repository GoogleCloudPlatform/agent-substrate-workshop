# Instructor runbook

**Current target:** `SUBSTRATE_REF=v1.0.0` (2026-11-06; not yet validated on a cluster). Last full cluster run: 2026-11-04 on `v1.0.0-rc.2`, GKE `1.37.1-gke.1697000` (Rapid, zonal, 2× `c3-standard-4`),
published and source install modes (`make e2e` passed with real Gemini; optional labs A, B, and J passed end-to-end on the cluster). Published images are used by default from `ghcr.io/agent-substrate/substrate` (`v1.0.0`).

## One week before

1. **Pick `SUBSTRATE_REF`**: the newest tag or commit that passes `make e2e` in **both** install modes,
   against a scratch GCP project (not an attendee's).
   - Published mode (default in `workshop.env.example`): `SUBSTRATE_IMAGE_REPO=ghcr.io/agent-substrate/substrate` and
     `SUBSTRATE_IMAGE_TAG=${SUBSTRATE_REF}`, so `install-substrate.sh` runs
     `ate-setup deploy ate-system --image-repo … --image-tag …` without building from source.
   - Source mode: leave `SUBSTRATE_IMAGE_REPO`/`SUBSTRATE_IMAGE_TAG` empty in `workshop.env` — `install-substrate.sh`
     clones `SUBSTRATE_REF` and builds `ate-setup`, `kubectl-ate`, `ateworker-gvisor`, and the Envoy dataplane from source.
   - `make e2e` drives every module through `catch-up` + `verify` (`scripts/catch-up.sh $n && scripts/verify/$((n-1)).sh`
     for n = 1..7). Run it start to finish in **each** mode; a pass in one mode does not imply a pass in the other,
     since the worker image (`ateworker-gvisor`) comes from a different path in each. Run it with `bin/` on your
     PATH (`export PATH="$PWD/bin:$PATH"`, as attendees do in Module 0), or `verify/00` fails its PATH check.
   - Put the validated ref in `workshop.env.example`'s `SUBSTRATE_REF` and in `app/go.mod`
     (`cd app && go get go.ate.dev/substrate@<ref> && go mod tidy`), since `ateapipb` (used by the
     gateway, `egressctl`, and the idle-suspender) must speak the same protocol as the control plane you install.
2. **Run the mock path too**: `MOCK_LLM=true make e2e`, so you know the whole workshop still works when Gemini is
   unreachable (quota, a locked-down project, a flaky network) — the fallback attendees are told about in Module 4.
   The gateway keeps that live `MOCK_LLM=true` afterwards; run `MOCK_LLM=false make gateway` to go back to Gemini.
3. **Check external dependencies that drift independently of substrate:**
   - `GEMINI_MODEL` (`workshop.env.example`, currently `gemini-3.8-flash`) is still served at `MODEL_LOCATION`
     (currently `global`).
   - `PROM_FRONTEND_IMAGE` (currently `gke.gcr.io/prometheus-engine/frontend:v0.15.3-gke.0`) is a current tag.
4. **Re-check [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) releases.** If it has
   gained auto suspend/resume or multi-sandbox-per-Pod, update the comparison table in
   [Module 1](../modules/01-architecture/README.md) and the wrap-up table in
   [Module 7](../modules/07-wrap-up/README.md).
5. **For optional lab A** (`agent-substrate/env`) and **optional lab B** (`google/ax`): verify that their pinned commits
   (`ENV_REF` and `AX_REF`) and `v1.0.0` patches (`optional/A-env/patch-env-v060.py` and `optional/B-ax/patch-ax-v040.py`) still build and pass unit tests against `SUBSTRATE_REF` (or drop the
   patch once upstream `main` catches up to `SUBSTRATE_REF`).
   **For optional lab H** (`moficodes/agent-swarm`, which must be public before the event): pick a commit whose `go.mod` substrate version is
   exactly `SUBSTRATE_REF`. Its `hack/deploy.sh` refuses to deploy onto a cluster running any other version.
   **For `ko`** (pinned to `github.com/google/ko@v0.19.1` in Setup and the preflight hint): bump the pin only to a release
   whose `go.mod` Go version is at or below Cloud Shell's Go version.
6. **Provision attendee projects and clusters.** Each attendee is Owner on their own GCP project, with a
   bare GKE cluster that has, at creation:
   - GKE **1.37+** (the minimum the workshop is validated on; `make preflight` rejects older). GKE 1.37 serves
     `certificates.k8s.io/v1` (`ClusterTrustBundle` and `PodCertificateRequest`) by default, which Substrate
     `v1.0.0` uses directly (`make preflight` checks that `v1` or `v1beta1` is served).
   - **Workload Identity** and **managed OpenTelemetry** with `--managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS`.
   - **Managed Prometheus** enabled (`--enable-managed-prometheus`) — Module 6's Prometheus UI reads through it.
   - Machine type **`c3-standard-4`**, 2 nodes, **no node auto-upgrade**, **no spot/preemptible nodes** (an
     upgrade or a spot reclaim mid-workshop disrupts running workers and loses whatever wasn't already snapshotted).

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
     --managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS
   # Start must be in the past (or now) for preflight to count it; cover the workshop day with the end.
   gcloud container clusters update "$CLUSTER_NAME" --project "$PROJECT_ID" --location us-central1-a \
     --add-maintenance-exclusion-name workshop-freeze \
     --add-maintenance-exclusion-start "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
     --add-maintenance-exclusion-end "$(date -u -d '+2 days' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+2d +%Y-%m-%dT%H:%M:%SZ)" \
     --add-maintenance-exclusion-scope no_minor_or_node_upgrades
   ```
   Projects that still may create unenrolled clusters can use `--release-channel None --no-enable-autoupgrade`
   instead and skip the exclusion.
   With a regional `--location`, `--num-nodes` is per zone (2 × 3 zones = 6 nodes). Put the cluster's
   location in each attendee's `workshop.env` as `CLUSTER_LOCATION`.

   Send each attendee their `PROJECT_ID` and `CLUSTER_LOCATION` (the example above: `us-central1-a`); both are
   required in `workshop.env`. Confirm they can `gcloud auth login` and reach the project before the day.

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
| 7 Wrap-up | 3 | Point everyone at labs A (`env`) and B (`google/ax`). | — |

That's 85 minutes with no slack. If a module runs long, take the "cut if behind" for that module rather than
compressing the next one — Module 6 in particular needs its 1–3 minute Cloud Monitoring lag no matter how fast
you talk.

If someone falls more than one module behind, have them run `make catch-up MODULE=<current>` and keep going.
Every module README starts from the previous module's verified end state (`make verify MODULE=<current - 1>`),
which is exactly what `catch-up.sh <current>` produces.

## After

Ask attendees to run `make teardown` (keeps their cluster, bucket, and Artifact Registry — only the in-cluster
workshop state goes away), or `YES=1 scripts/teardown.sh --all` if the whole project is being torn down too
(also deletes the bucket, the Artifact Registry repo, the snapshot transfer service account, and the
workshop namespace's project IAM bindings `make gcp` created). Artifact Registry read access is granted on the
workshop repo only, so no other cluster in the project loses image pulls. Neither
one touches the GKE cluster (`make delete-cluster` deletes the cluster after releasing its PersistentVolumeClaims and their disks), and neither deletes Substrate's own `ate-system` Deployments/DaemonSets — but
**`--all` leaves the already-running Substrate unable to actually work**: in source install mode its
control-plane and worker images lived in the Artifact Registry repo that was just deleted, and the IAM grants
that let `atelet`/`ateapi` write snapshots are gone too. If the project is being kept around rather than
deleted outright, `--all` needs a full `make install` (or `make catch-up`) afterward before Substrate is usable
again.
