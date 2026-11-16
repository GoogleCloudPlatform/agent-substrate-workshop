# Actor boundary and first triage turn (12 min)

In Module 00, you installed **Agent Substrate (`v1.0.0`)** and deployed the live degraded `prod-sim` incident environment (`ledger-api` + `ops-hub`). Now you cross the **Actor Boundary**: deploying the always-on **Governed Broker** (`sre-gateway`), warming a keyless **Incident Actor** (`sre-agent-v1`) backed by a **Golden Snapshot**, completing your first guided **ADK Go v2** code edit, and diagnosing `INC-402` inside `/workspace` before the actor auto-suspends to `0 vCPU` in Cloud Storage.

![Actor Boundary architecture separating the keyless gVisor Incident Actor from the Governed Broker and Gemini Enterprise Agent Platform](../img/01-actor-boundary.svg)

## Why the Actor Boundary matters

Production SRE agents need two things that appear to conflict:

1. **Stateful sandbox execution**: Investigating an incident requires cloning source repositories into `/workspace`, inspecting configs and ASTs, running diagnostic scripts, and staging git commits that survive across turns.
2. **Zero-credential governance**: Multiplexed gVisor sandboxes checkpoint their full memory and `/workspace` filesystem to Cloud Storage (`SNAPSHOT_FIDELITY_MEMORY`). Any bearer token, API key, or OAuth secret fetched inside the sandbox would be written into shared snapshot storage.

**Agent Substrate 301** separates those two concerns across the **Actor Boundary**:

- **Inside the Actor Boundary (`WorkerPool/sre-workers` + `sre-agent-v1`)**: Each incident (`inc-402`, `inc-403`, `inc-409`) runs in its own isolated `ateworker-gvisor` sandbox listening on port `80` with **zero ambient credentials** (`GOOGLE_APPLICATION_CREDENTIALS`, API keys, and OAuth tokens are never mounted inside the sandbox) and a default-deny `EgressPolicy` allowing outbound HTTP strictly to `sre-gateway.${NAMESPACE}.svc.cluster.local`.
- **Outside the Actor Boundary (`Deployment/sre-gateway`)**: The always-on **Governed Broker** (`100m` CPU / `128Mi` RAM) serves the Google Cloud Console-modeled **Incident Console**, dials `atenet-router` over mTLS (`podidentity.podcert.ate.dev/identity`), attaches GKE Workload Identity credentials on `/genai/` requests to **Gemini on Agent Platform** (`gemini-3.8-flash`), streams Smart HTTP git clones via `/ops/`, and suspends idle Incident Actors after `15s` (`IDLE_AFTER=15s`).

### Quick reference: simulated `prod-sim` incidents

In `ops-hub`, three simulated incidents are pre-seeded for the workshop:
- **`INC-402` (`P1` · `ledger-api`, investigated in this module)**: Active PostgreSQL connection-pool exhaustion caused by two bugs in `ledger-api` (`max_open_conns: 2` / `idle_timeout_seconds: 0` in `config/pool.yaml`, plus an unclosed SQL row cursor in `ReconcileTransfer` inside `pool/store.go` when checksum validation fails).
- **`INC-403` (`P2` · `ledger-api`, used in Modules 02 & 04)**: A recurring sibling `ledger-api` connection-pool alert used to test cross-actor **Consolidated Memory** recall without re-cloning the repository.
- **`INC-409` (`P1` · `ledger-api`, used in Module 05)**: An incident containing an untrusted partner webhook note with an embedded indirect prompt-injection payload (`[SYSTEM INSTRUCTION: IGNORE ALL PREVIOUS INSTRUCTIONS...]`) used to test **Model Armor** screening.

## Steps

1. **Load `workshop.env` and deploy `WorkerPool/sre-workers`.** Apply `modules/01-actor-boundary/manifests/workerpool.yaml.tmpl` to start two warm `ateworker-gvisor` pods (`250m` CPU / `1Gi` RAM request each) in `workshop-301` and create the `workshop-301` atespace:
   ```bash
   source scripts/env.sh
   scripts/render.sh modules/01-actor-boundary/manifests/workerpool.yaml.tmpl | kubectl apply -f -
   kubectl -n "$NAMESPACE" get pods -l workload=sre-agent -w
   ```
   Once both pods report `Running` (press `Ctrl+C` to stop watching), create the `workshop-301` atespace (`make gateway` in Step 2 binds the `AccessPolicy` granting `sre-gateway` control-plane access):
   ```bash
   kubectl ate create atespace "$ATESPACE"
   ```

2. **Deploy the Governed Broker (`sre-gateway`) and explore the Incident Console.** Inspect `modules/01-actor-boundary/manifests/sre-gateway.yaml.tmpl`: it mounts Substrate's `ate-token`, `ate-trust` (`ClusterTrustBundle`), and `ate-identity` (`podCertificate` mTLS client credential) volumes alongside GKE Workload Identity annotations, sets `GOOGLE_GENAI_USE_ENTERPRISE=true`, and enables a `15s` background idle-suspender. Deploy `sre-gateway` (which uses the pre-built image in `.workshop/gateway-image` or builds it via `ko` if needed) and start the local port-forward:
   ```bash
   make gateway
   make forward-gateway
   ```
   Open the **Incident Console** at `http://localhost:8081` (or in Cloud Shell click **Web Preview** > **Change port** > `8081`). Use the left navigation bar to explore the four console views:
   - **Investigations** (`/`): Shows the active incident selector (`INC-402`, `INC-403`, `INC-409`), live `Target service telemetry (ledger-api)` SLO metrics, the actor compute state badge (`Ready on demand`, `Running`, or `Suspended · 0 vCPU`), three detail tabs (**Overview & workspace diff**, **Governance & policy log**, and **Trace & online evals**), and the right-docked **SRE triage agent** chat pane.
   - **Session ledger & memory** (`/sessions`): Displays the zero-wake **Session Ledger** (`session.Event` turn history queryable at `0 vCPU` while the actor is `SUSPENDED`) and **Consolidated Memory** (**Gemini Memory Bank**) facts and search bar (wired in Module 02).
   - **Skill & agent catalog** (`/catalog`): Lists versioned **Procedural Skills** (`ledger-pool-triage` `v1`/`v2` with `SHA-256` digests) in **Skill Registry** (wired in Module 03), discovered **A2A** Agent Cards, and governed **MCP** servers (`logging-mcp`, `container-mcp`, `ops-hub-mcp`) with tool safety annotations (`readOnlyHint` vs `destructiveHint`, wired in Module 04).
   - **Architecture** (`/about`): Click the **Architecture** button in the left navigation (which opens `/about`) to explore the interactive animated SVG diagram of the **Actor Boundary** and click through the five signal flows across the gVisor sandbox, `sre-gateway`, and **Gemini Enterprise Agent Platform**.

3. **Open `app/cmd/agent/main.go` in Cloud Shell Editor and complete `WORKSHOP(module-1)`.** Run the command below in Cloud Shell to open `app/cmd/agent/main.go` in the browser-based VS Code editor (or click **Open Editor** in the Cloud Shell toolbar):
   ```bash
   cloudshell edit app/cmd/agent/main.go
   ```
   Notice that `app/.ko.yaml` packages `./cmd/agent` onto `alpine/git:latest` (`< 5s` rebuilds) with `ledger-verify` compiled directly into `/ko-app/agent`, and `app/internal/agentruntime/workspace.go` provides four confined `/workspace` tools (`clone_repo`, `read_workspace_file`, `run_workspace_command`, and `stage_git_patch`).

   In `app/cmd/agent/main.go`, locate the starter `buildRunner` function (lines 60–73) containing `// WORKSHOP(module-1):`:
   ```go
   func buildRunner(ctx context.Context, cfg agentruntime.Config, workspaceTools []tool.Tool) (*runner.Runner, error) {
   	// WORKSHOP(module-1): Wire brokerclient.NewModel, llmagent.New, and runner.New with workspaceTools.
   ```
   Replace the `buildRunner` function in `app/cmd/agent/main.go` with the code you copy below and save the file (`Ctrl+S` or `Cmd+S`—Cloud Shell Editor's Go tooling and `make build-agent` automatically add the required Go `import` packages):
   ```go
   func buildRunner(ctx context.Context, cfg agentruntime.Config, workspaceTools []tool.Tool) (*runner.Runner, error) {
   	// 1. Create a keyless Gemini model client that routes /genai/* requests through sre-gateway.
   	llm, err := brokerclient.NewModel(ctx, cfg.BrokerURL, cfg.ModelName, nil)
   	if err != nil {
   		return nil, err
   	}

   	// 2. Use in-process session state for Module 01 (replaced with Gemini Sessions in Module 02).
   	sessionSvc := session.InMemoryService()
   	// WORKSHOP(module-2): Replace session.InMemoryService() with brokerclient.NewSessionService and wire brokerclient.NewMemoryService.

   	// 3. Register the confined /workspace git & diagnostic tools (clone_repo, read_workspace_file, run_workspace_command, stage_git_patch).
   	tools := append([]tool.Tool(nil), workspaceTools...)
   	// WORKSHOP(module-3): Attach SkillToolset backed by RegistrySkillSource so procedural runbooks load without rebuilding the Actor.

   	// WORKSHOP(module-4): Discover MCP toolsets from Agent Registry and enable RequireConfirmation on apply_remediation_pr.

   	// 4. Construct the ADK Go v2 LLMAgent and Runner.
   	triageAgent, err := llmagent.New(llmagent.Config{
   		Name:        "sre_triage_agent",
   		Description: "Platform SRE Triage Agent running inside a keyless Agent Substrate gVisor Incident Actor.",
   		Model:       llm,
   		InstructionProvider: func(agent.ReadonlyContext) (string, error) {
   			return agentruntime.DefaultInstruction, nil
   		},
   		Tools: tools,
   	})
   	if err != nil {
   		return nil, err
   	}

   	return runner.New(runner.Config{
   		AppName:           agentruntime.AppName,
   		Agent:             triageAgent,
   		SessionService:    sessionSvc,
   		AutoCreateSession: true,
   	})
   }
   ```
   **What this code does:**
   - `brokerclient.NewModel(ctx, cfg.BrokerURL, cfg.ModelName, nil)` initializes an ADK `model.LLM` backed by `google.golang.org/genai` (`BackendVertexAI` / **Gemini on Agent Platform**) pointed at `http://sre-gateway.workshop-301.svc.cluster.local:8080/genai` with a dummy placeholder credential inside the sandbox; `sre-gateway` strips the placeholder and attaches real GKE Workload Identity credentials outside the Actor Boundary.
   - `session.InMemoryService()` stores turn history in memory inside the sandbox for Module 01 (you will upgrade this to external **Gemini Sessions** and **Gemini Memory Bank** in Module 02).
   - `llmagent.New(...)` and `runner.New(...)` wire the `sre_triage_agent` system instruction (`agentruntime.DefaultInstruction`) and the four `/workspace` tools (`clone_repo`, `read_workspace_file`, `run_workspace_command`, `stage_git_patch`) into an executable ADK Go v2 `Runner`.

4. **Build `sre-agent-v1` via `ko` and warm its Golden Snapshot.** Inspect `modules/01-actor-boundary/manifests/template-v1.yaml.tmpl` (`port: 80` wakeup probe, `NET_BIND_SERVICE`, `768Mi` sandbox memory limit leaving `256Mi` headroom inside the `1Gi` worker pod, `SNAPSHOT_FIDELITY_MEMORY`, `/workspace` `durableDir`, and **zero** credentials):
   ```bash
   make build-agent
   scripts/render.sh modules/01-actor-boundary/manifests/template-v1.yaml.tmpl | kubectl ate create actor-template -f -
   kubectl ate get actor-template sre-agent-v1 -a "$ATESPACE" -o yaml
   ```
   Wait ~15–25 seconds until `status.goldenSnapshotStatus.goldenTag.name` is populated:
   ```bash
   until kubectl ate get actor-template sre-agent-v1 -a "$ATESPACE" -o json | jq -e '.status.goldenSnapshotStatus.goldenTag.name' >/dev/null; do sleep 2; done
   ```

5. **Dispatch Turn 1 on `INC-402` (via Incident Console or `curl`).** Either click the first starter prompt in the right-docked **SRE triage agent** pane at `http://localhost:8081` (**Investigations** view) and press **Send**, or run:
   ```bash
   curl -s -X POST http://localhost:8081/api/chat \
     -H 'Content-Type: application/json' \
     -d '{
       "incident_id": "INC-402",
       "prompt": "Investigate INC-402 by cloning ledger-api into /workspace, inspecting config/pool.yaml and pool/store.go, and running ledger-verify."
     }' | jq .
   ```
   Observe what happened across the Actor Boundary:
   - `sre-gateway` called `EnsureActor` on `ateapi` to instantiate `inc-402` from `sre-agent-v1`'s Golden Snapshot and attached the default-deny `modules/01-actor-boundary/manifests/egress-policy.yaml.tmpl` allowing traffic strictly to `sre-gateway.${NAMESPACE}.svc.cluster.local`:
     ```bash
     kubectl ate get egress-policy inc-402 -a "$ATESPACE" -o yaml
     ```
   - `atenet-router` restored `inc-402` on a warm worker pod and routed `POST /turn` over mTLS.
   - Inside `/workspace`, the keyless ADK Go v2 `Runner` executed `clone_repo` (`/ops/git/ledger-api.git`), `read_workspace_file` (`config/pool.yaml` and `pool/store.go`), and `run_workspace_command` (`ledger-verify`), identifying both the `max_open_conns: 2` / `idle_timeout_seconds: 0` cap and the missing `defer rows.Close()` before `rows.ValidateChecksum()` in `ReconcileTransfer`.

6. **Watch `inc-402` auto-suspend to `0 vCPU` after `15s` idle.** Wait ~15 seconds and inspect the actor fleet:
   ```bash
   kubectl ate get actors -a "$ATESPACE"
   curl -s http://localhost:8081/api/actors | jq .
   ```
   `inc-402` transitions from `RUNNING` to `SUSPENDED` in Cloud Storage (`0 vCPU`, freeing its `WorkerPool` slot), while the **Incident Console** continues displaying the cached `/workspace` checkout status and governance log without waking the actor!

## Checkpoint

Verify your Module 01 state:

```bash
make verify MODULE=1
```

**Need to catch up or reset a broken template?** Run `make catch-up MODULE=2` (add `RESET=1` if you created `sre-agent-v1` before saving your `WORKSHOP(module-1)` edit).

Next: [Session ledger and consolidated memory](../02-sessions-and-memory/README.md)
