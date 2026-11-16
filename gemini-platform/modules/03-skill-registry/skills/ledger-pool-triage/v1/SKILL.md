---
name: ledger-pool-triage
description: Diagnose connection pool sizing and idle timeout saturation risks in ledger-api (config/pool.yaml).
license: Apache-2.0
compatibility: POSIX sh inside /workspace/ledger-api
metadata:
  revision: v1
  service: ledger-api
allowed-tools: clone_repo read_workspace_file run_workspace_command
---

# Ledger Pool Triage Runbook (Revision v1)

Follow these exact steps inside the Incident Actor sandbox to run the baseline connection pool diagnostic against `ledger-api`:

1. Ensure the `ledger-api` repository is cloned into `/workspace/ledger-api` by calling `clone_repo` (`repo="ledger-api"`).
2. Execute the unpacked Procedural Skill diagnostic script via `run_workspace_command`:
   - `command`: `"sh"`
   - `args`: `[".skills/ledger-pool-triage/scripts/diagnose_pool.sh"]`
   - `dir`: `"ledger-api"`
3. Report the `SKILL_REVISION=v1` key-value and JSON output, specifically `MAX_OPEN_CONNS`, `IDLE_TIMEOUT_SECONDS`, `ACQUIRE_TIMEOUT_MS`, and `POOL_CONFIG_STATUS`.
