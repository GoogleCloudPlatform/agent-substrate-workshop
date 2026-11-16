---
name: ledger-pool-triage
description: Diagnose connection pool sizing in config/pool.yaml and unclosed row cursor leaks (rows.Close) across ledger-api Go source files.
license: Apache-2.0
compatibility: POSIX sh inside /workspace/ledger-api
metadata:
  revision: v2
  service: ledger-api
allowed-tools: clone_repo read_workspace_file run_workspace_command
---

# Ledger Pool Triage Runbook (Revision v2)

Follow these exact steps inside the Incident Actor sandbox to run both connection pool sizing checks and Go row-cursor leak analysis against `ledger-api`:

1. Ensure the `ledger-api` repository is cloned into `/workspace/ledger-api` by calling `clone_repo` (`repo="ledger-api"`).
2. Execute the unpacked Procedural Skill diagnostic script via `run_workspace_command`:
   - `command`: `"sh"`
   - `args`: `[".skills/ledger-pool-triage/scripts/diagnose_pool.sh"]`
   - `dir`: `"ledger-api"`
3. Report the `SKILL_REVISION=v2` findings, covering both `config/pool.yaml` (`MAX_OPEN_CONNS`, `IDLE_TIMEOUT_SECONDS`) and Go source cursor-release checks (`CURSOR_LEAK_DETECTED`, `CURSOR_LEAK_LOCATION`, and whether `defer rows.Close()` runs before `rows.ValidateChecksum()` in `ReconcileTransfer`).
