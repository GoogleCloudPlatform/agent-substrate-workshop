# 4. Uniform-CPU Classroom Sizing and Quota-Safe `ComputeClass`

Status: accepted

We size the entire workshop runtime footprint (`WorkerPool` `2× 250m CPU / 1Gi`, `sre-gateway` `100m / 128Mi`, `prod-sim/ledger-api` `50m / 64Mi`, and `prod-sim/ops-hub` `50m / 64Mi` = `700m` CPU / `2.25Gi` RAM total) to schedule directly onto pre-provisioned `2× 4-vCPU` GKE `1.37+` clusters without scaling up extra nodes, and ship `ComputeClass/substrate-gvisor` with `nodePoolAutoCreation.enabled: false` by default (`ENABLE_COMPUTE_CLASS_AUTOPROVISION=true` opt-in).

## Context & Rationale

1. **Google Skills (`gcp_medium_extra`) vs. BYO Codelab limits**: Self-paced Codelab users create `2× c3-standard-4` nodes, whereas Google Skills (`CloudVLab/gcp-devrel-content`) provisions `2× n1-standard-4` (`min_cpu_platform = "Intel Skylake"`, `100 GB pd-balanced`, with CPU-pinned `n2-standard-4` fallback) under an `8 vCPU` cap and a `~700 GB` regional SSD quota (already ~`700 GB` used once Substrate's `500 GiB` Postgres PVC binds), disallowing `c3` and `n4`.
2. **Preserving cross-worker `SNAPSHOT_FIDELITY_MEMORY` restores**: `make preflight` accepts any uniform-CPU GKE `1.37+` node pool (`c3-*`, `n4-*`, CPU-pinned `n2-*`, or Skylake-pinned `n1-*`) and rejects/warns on `e2-*` mixed Intel/AMD node pools that would downgrade cross-worker memory restores to cold restarts. Keeping `ComputeClass` node auto-creation opt-in lets every attendee apply and inspect the real `ComputeClass/substrate-gvisor` resource without triggering out-of-quota node provisioning in Qwiklabs.
