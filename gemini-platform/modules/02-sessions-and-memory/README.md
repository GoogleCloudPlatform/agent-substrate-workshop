# Session ledger and consolidated memory (15 min)

In Module 01, your keyless **Incident Actor** (`sre-agent-v1`) cloned `ledger-api.git` into `/workspace`, diagnosed `INC-402`, and suspended to `0 vCPU` in Cloud Storage (`SNAPSHOT_FIDELITY_MEMORY`). Now you solve what happens outside a single sandbox checkpoint: persisting structured turn events to a managed **Session Ledger** (**Gemini Sessions**) for zero-wake (`0 vCPU`) inspection and extracting **Consolidated Memory** (**Gemini Memory Bank**) that survives `ActorTemplate` (`v1` -> `v2`) rollouts and transfers across sibling Incident Actors (`inc-403`).

![Three-tier state model comparing raw Cloud Storage snapshots, Gemini Sessions zero-wake ledger, and Gemini Memory Bank recall](../img/02-sessions-and-memory.svg)

## Snapshots vs. Session Ledger vs. Consolidated Memory

Agent Substrate's Cloud Storage checkpoints and **Gemini Enterprise Agent Platform**'s state services solve distinct layers of stateful agent operations:

| Layer | Where it lives | What it preserves | Lifecycle & visibility |
|---|---|---|---|
| **GCS RAM + `/workspace` snapshot** (`SNAPSHOT_FIDELITY_MEMORY`) | Inside the **Actor Boundary** (`gs://${BUCKET_NAME}`) | Cloned git working tree, unpacked diagnostic scripts, staged commits, and in-process memory | Scoped to one `Actor` and one immutable `ActorTemplate`; reading inside `/workspace` wakes a worker slot |
| **Session Ledger** (**Gemini Sessions**) | Outside the **Actor Boundary** via `sre-gateway/agentengine` | Ordered `session.Event` log: operator prompts, ADK tool calls/results, and scoped `StateDelta` updates | Queryable at `0 vCPU` while the Actor is `SUSPENDED`; survives `v1` -> `v2` template replacement |
| **Consolidated Memory** (**Gemini Memory Bank**) | Outside the **Actor Boundary** via `sre-gateway/agentengine` | Distilled root-cause and operational runbook facts extracted after completed turns (`AddSessionToMemory`) | Shared across upgraded templates (`sre-agent-v2`) and sibling Incident Actors (`inc-402` -> `inc-403`) |

Because `app/cmd/agent` remains **100% keyless**, `brokerclient.NewSessionService` and `brokerclient.NewMemoryService` speak over HTTP exclusively to `sre-gateway/agentengine/*`. Outside the sandbox, `sre-gateway` attaches Workload Identity / Pod **Agent Identity** credentials when delegating to a provisioned `ReasoningEngine` runtime (from `.workshop/reasoning-engine-id`) and maintains a synchronized durable broker ledger when running without one.

## Steps

1. **Open `app/cmd/agent/main.go` in Cloud Shell Editor and complete `WORKSHOP(module-2)`.** Run the command below in Cloud Shell to open `app/cmd/agent/main.go` in the editor:
   ```bash
   cloudshell edit app/cmd/agent/main.go
   ```
   In `app/cmd/agent/main.go`, select the **entire `buildRunner` function** (from `func buildRunner(...)` through its closing `}`) and replace it with the complete `buildRunner` function below, then save the file (`Ctrl+S` or `Cmd+S`—Cloud Shell Editor's Go tooling and `make build-agent` automatically update the Go `import` block for you):
   ```go
   func buildRunner(ctx context.Context, cfg agentruntime.Config, workspaceTools []tool.Tool) (*runner.Runner, error) {
   	llm, err := brokerclient.NewModel(ctx, cfg.BrokerURL, cfg.ModelName, nil)
   	if err != nil {
   		return nil, err
   	}

   	// 1. Replace in-memory sessions with Gemini Sessions + Gemini Memory Bank via sre-gateway/agentengine.
   	sessionSvc, err := brokerclient.NewSessionService(cfg.BrokerURL, nil)
   	if err != nil {
   		return nil, err
   	}
   	memorySvc, err := brokerclient.NewMemoryService(cfg.BrokerURL, nil)
   	if err != nil {
   		return nil, err
   	}
   	memoryPlugin, err := brokerclient.NewMemoryConsolidationPlugin(sessionSvc, memorySvc)
   	if err != nil {
   		return nil, err
   	}

   	// 2. Attach ADK Go v2's load_memory and preload_memory tools alongside /workspace tools.
   	tools := append([]tool.Tool(nil), workspaceTools...)
   	tools = append(tools, loadmemorytool.New(), preloadmemorytool.New())
   	// WORKSHOP(module-3): Attach SkillToolset backed by RegistrySkillSource so procedural runbooks load without rebuilding the Actor.

   	// WORKSHOP(module-4): Discover MCP toolsets from Agent Registry and enable RequireConfirmation on apply_remediation_pr.

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
   		MemoryService:     memorySvc,
   		PluginConfig:      runner.PluginConfig{Plugins: []*plugin.Plugin{memoryPlugin}},
   		AutoCreateSession: true,
   	})
   }
   ```
   **What this code does:**
   - `brokerclient.NewSessionService(cfg.BrokerURL, nil)` streams every ADK `session.Event` (user prompts, tool calls, tool responses, and `StateDelta` updates) across the Actor Boundary to `sre-gateway/agentengine/sessions` (**Gemini Sessions**), so operators can inspect turn history at `0 vCPU` while the actor is `SUSPENDED`.
   - `brokerclient.NewMemoryService(cfg.BrokerURL, nil)` and `brokerclient.NewMemoryConsolidationPlugin(sessionSvc, memorySvc)` automatically call `AddSessionToMemory` after each completed turn, distilling verified root-cause facts into **Gemini Memory Bank**.
   - `preloadmemorytool.New()` and `loadmemorytool.New()` allow upgraded templates (`sre-agent-v2`) and sibling Incident Actors (`inc-403`) to recall prior `ledger-api` root causes without re-cloning the git repository.

2. **Build `sre-agent-v2`, warm its Golden Snapshot, and roll out the `v2` template on `sre-gateway`.** Inspect `modules/02-sessions-and-memory/manifests/template-v2.yaml.tmpl`, build the updated agent image with `ko`, and create `ActorTemplate/sre-agent-v2`:
   ```bash
   source scripts/env.sh
   make build-agent
   scripts/render.sh modules/02-sessions-and-memory/manifests/template-v2.yaml.tmpl | kubectl ate create actor-template -f -
   until kubectl ate get actor-template sre-agent-v2 -a "$ATESPACE" -o json | jq -e '.status.goldenSnapshotStatus.goldenTag.name' >/dev/null; do sleep 2; done
   ```
   Point `sre-gateway` at `sre-agent-v2` (which also picks up `.workshop/reasoning-engine-id` automatically when your background runtime bootstrap finished) and reconnect the local port-forward:
   ```bash
   DEFAULT_ACTOR_TEMPLATE=sre-agent-v2 make gateway
   make forward-gateway
   ```

3. **Dispatch an investigation turn on `INC-402` using `sre-agent-v2` to record the Session Ledger.** Run the `curl` command below to send a turn to `inc-402` on template `sre-agent-v2`:
   ```bash
   curl -s -X POST http://localhost:8081/api/chat \
     -H 'Content-Type: application/json' \
     -d '{
       "incident_id": "INC-402",
       "template": "sre-agent-v2",
       "prompt": "Investigate INC-402 by cloning ledger-api into /workspace, inspecting config/pool.yaml and pool/store.go, and running ledger-verify."
     }' | jq '{actor_name, template, workspace_cloned: .workspace.cloned, tool_steps: [(.tool_steps // [])[].name]}'
   ```
   Expected output on the first `sre-agent-v2` turn (when `/workspace/ledger-api` starts empty):
   ```json
   {
     "actor_name": "inc-402",
     "template": "sre-agent-v2",
     "workspace_cloned": true,
     "tool_steps": [
       "clone_repo",
       "read_workspace_file",
       "read_workspace_file",
       "run_workspace_command"
     ]
   }
   ```
   *(If `inc-402` on `sre-agent-v2` already cloned `/workspace/ledger-api` and recorded its file inspection in the Session Ledger on an earlier turn, `preload_memory` injects those facts into the prompt automatically and `tool_steps` only needs to run `["run_workspace_command"]` while `workspace_cloned` remains `true`:)*
   ```json
   {
     "actor_name": "inc-402",
     "template": "sre-agent-v2",
     "workspace_cloned": true,
     "tool_steps": [
       "run_workspace_command"
     ]
   }
   ```
   **What this output means (and what to notice after running the command):**
   - `template: "sre-agent-v2"` and `workspace_cloned: true`: Confirm that `inc-402` is running the upgraded `sre-agent-v2` ADK Go v2 runner with `/workspace/ledger-api` checked out. *(If you ever see `workspace_cloned: false` and `tool_steps: []`, `app/cmd/agent/main.go` was not saved before `make build-agent`—save `app/cmd/agent/main.go` in Cloud Shell Editor and re-run Step 2.)*
   - **Notice why `sre-agent-v1` -> `sre-agent-v2` replaced the raw memory snapshot**: In Agent Substrate, an `Actor`'s `actorTemplate` reference is immutable because RAM checkpoints bind to the exact container image and kernel state of that template. Upgrading `inc-402` from `sre-agent-v1` -> `sre-agent-v2` replaced the `sre-agent-v1` actor with a fresh `sre-agent-v2` sandbox restored from `sre-agent-v2`'s Golden Snapshot.
   - **Persisted outside the sandbox**: During this turn, `Runner.Run` streamed every prompt, tool call, and model reply through `sre-gateway/agentengine/sessions:appendEvent` into the **Session Ledger**, and `NewMemoryConsolidationPlugin` called `AddSessionToMemory` to distill `INC-402`'s verified root cause (`MAX_OPEN_CONNS=2`, `IDLE_TIMEOUT_SECONDS=0` in `config/pool.yaml` and the missing `defer rows.Close()` before `rows.ValidateChecksum()` in `pool/store.go`) into **Consolidated Memory**.

4. **Audit the Session Ledger at `0 vCPU` (zero-wake inspection).** Suspend `inc-402` (or wait `15s` for the idle-suspender) and confirm it consumes zero worker slots:
   ```bash
   kubectl ate suspend actor inc-402 -a "$ATESPACE"
   kubectl ate get actors -a "$ATESPACE"
   ```
   Now query `inc-402`'s structured turn history through `sre-gateway` and verify the Actor remains `SUSPENDED`:
   ```bash
   curl -s http://localhost:8081/api/ledger/sessions/inc-402 | jq '{session_id, actor_state, compute_badge, event_count, events: [(.events // [])[] | {kind, author, summary}]}'
   kubectl ate get actor inc-402 -a "$ATESPACE" -o json | jq -r '.status.state'
   ```
   Notice that `actor_state` is `"SUSPENDED"` (`Suspended · 0 vCPU · <400ms wake`), while `event_count` and `events` return the full ordered log of user prompts, tool calls, and model replies directly from the **Session Ledger** without waking the actor.
   Open `http://localhost:8081/sessions` (**Session ledger & memory** in the left navigation of the **Incident Console**):
   - Click **Inspect events** on `inc-402` to expand each structured prompt, tool call, and tool response while the **Actor compute** badge stays `Suspended · 0 vCPU · < 400ms wake`.
   - Use the **Consolidated memory** search bar at the bottom of `/sessions` (for example, search `MAX_OPEN_CONNS` or `rows.Close`) to test live `SearchMemory` recall.

5. **Prove cross-rollout survival and sibling Incident Actor (`INC-403`) recall.** First, delete `inc-402` to discard its sandbox `/workspace` and raw checkpoint completely, wait for deletion to finish, and ask a newly instantiated `inc-402` (`sre-agent-v2`) to summarize `INC-402` from memory without re-cloning:
   ```bash
   kubectl ate delete actor inc-402 -a "$ATESPACE"
   while kubectl ate get actor inc-402 -a "$ATESPACE" >/dev/null 2>&1; do sleep 1; done
   curl -s -X POST http://localhost:8081/api/chat \
     -H 'Content-Type: application/json' \
     -d '{
       "incident_id": "INC-402",
       "template": "sre-agent-v2",
       "prompt": "Summarize the previously verified INC-402 connection pool root cause and tuning values from memory without re-cloning."
     }' | jq '{actor_name, template, workspace_cloned: .workspace.cloned, tool_steps: [(.tool_steps // [])[].name], reply}'
   ```
   **What this output means:**
   - `workspace_cloned: false` and `tool_steps` contains no `clone_repo`: Because you deleted `inc-402` above, its raw Cloud Storage `/workspace` snapshot was destroyed. Yet the fresh `sre-agent-v2` instance recovers both its **Session Ledger** history and its distilled **Consolidated Memory** facts (`MAX_OPEN_CONNS=2`, `IDLE_TIMEOUT_SECONDS=0`, `max_open_conns: 32`, `idle_timeout_seconds: 30`) across the Actor Boundary and answers immediately without cloning `ledger-api.git`!

   Next, dispatch a brand-new sibling Incident Actor for `INC-403` (`inc-403`, either by clicking the **Recall memory (INC-403)** preset chip in the **SRE triage agent** pane and pressing **Send**, or via `curl`):
   ```bash
   curl -s -X POST http://localhost:8081/api/chat \
     -H 'Content-Type: application/json' \
     -d '{
       "incident_id": "INC-403",
       "template": "sre-agent-v2",
       "prompt": "Triage sibling incident INC-403 using Consolidated Memory from prior ledger-api incidents."
     }' | jq '{actor_name, template, tool_steps: [(.tool_steps // [])[].name], reply}'
   ```
   `inc-403` recalls the consolidated `INC-402` pool-sizing and cursor-leak facts across the **Actor Boundary** via `preload_memory` (which injects distilled memories into the prompt with zero runtime tool calls) and/or `load_memory`, diagnosing `INC-403` immediately without repeating a repository clone.

## Checkpoint

Verify your Module 02 state:

```bash
make verify MODULE=2
```

**Need to catch up or reset `sre-agent-v2`?** Run `make catch-up MODULE=3` (add `RESET=1` to rebuild `sre-agent-v2` from `modules/02-sessions-and-memory/solution/main.go`).

Next: [Dynamic procedural skills with Skill Registry](../03-skill-registry/README.md)
