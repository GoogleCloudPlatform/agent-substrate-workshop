// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agentruntime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/agentruntime"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/brokerclient"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/opshub"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
)

func TestStarterStubAndReadyz(t *testing.T) {
	wsDir := t.TempDir()
	srv, err := agentruntime.NewServer(agentruntime.Config{
		WorkspaceDir: wsDir,
		TemplateName: "sre-agent-v1",
		BuildRunner: func(context.Context, agentruntime.Config, []tool.Tool) (*runner.Runner, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	readyResp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer readyResp.Body.Close()
	if readyResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /readyz status = %d, want 200", readyResp.StatusCode)
	}
	if got := readyResp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}

	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/turn", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE /turn: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /turn status = %d, want 405", delResp.StatusCode)
	}

	body, _ := json.Marshal(agentruntime.TurnRequest{
		IncidentID: "INC-402",
		SessionID:  "sess-stub",
		UserID:     "operator",
		Prompt:     "Investigate INC-402",
	})
	turnResp, err := http.Post(ts.URL+"/turn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /turn: %v", err)
	}
	defer turnResp.Body.Close()
	if turnResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /turn status = %d, want 200", turnResp.StatusCode)
	}
	var got agentruntime.TurnResponse
	if err := json.NewDecoder(turnResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode TurnResponse: %v", err)
	}
	if !got.StarterStub {
		t.Errorf("StarterStub = false, want true")
	}
	if !strings.Contains(got.Reply, "WORKSHOP(module-1)") {
		t.Errorf("Reply = %q, want mention of WORKSHOP(module-1)", got.Reply)
	}
}

func TestKeylessADKRunnerExecutesWorkspaceToolsAndEnforcesBoundaries(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_GENAI_USE_ENTERPRISE", "")

	opsSrv, err := opshub.NewServer(opshub.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opshub.NewServer: %v", err)
	}
	opsHandler := opsSrv.Handler()

	var (
		mu       sync.Mutex
		callIdx  int
		authHdrs []string
	)

	fakeBroker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/ops/"):
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/ops")
			opsHandler.ServeHTTP(w, r2)
			return
		case strings.HasPrefix(r.URL.Path, "/genai/"):
			mu.Lock()
			step := callIdx
			callIdx++
			authHdrs = append(authHdrs, r.Header.Get("Authorization"))
			mu.Unlock()

			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")

			var parts []map[string]any
			switch step {
			case 0:
				parts = []map[string]any{
					{"functionCall": map[string]any{
						"id":   "call-clone",
						"name": "clone_repo",
						"args": map[string]any{"repo": "ledger-api"},
					}},
				}
			case 1:
				parts = []map[string]any{
					{"functionCall": map[string]any{
						"id":   "call-read-escape",
						"name": "read_workspace_file",
						"args": map[string]any{"path": "../../etc/hosts"},
					}},
					{"functionCall": map[string]any{
						"id":   "call-read-pool",
						"name": "read_workspace_file",
						"args": map[string]any{"path": "ledger-api/config/pool.yaml"},
					}},
				}
			case 2:
				parts = []map[string]any{
					{"functionCall": map[string]any{
						"id":   "call-cmd-blocked",
						"name": "run_workspace_command",
						"args": map[string]any{"command": "rm", "args": []string{"-rf", "/"}, "dir": "ledger-api"},
					}},
					{"functionCall": map[string]any{
						"id":   "call-cmd-verify",
						"name": "run_workspace_command",
						"args": map[string]any{"command": "ledger-verify", "dir": "ledger-api"},
					}},
				}
			case 3:
				parts = []map[string]any{
					{"functionCall": map[string]any{
						"id":   "call-stage",
						"name": "stage_git_patch",
						"args": map[string]any{
							"branch":         "fix/inc-402-pool",
							"commit_message": "Fix connection pool sizing and close rows cursor on checksum error",
							"files": map[string]string{
								"config/pool.yaml": "max_open_conns: 32\nidle_timeout_seconds: 30\nacquire_timeout_ms: 250\n",
								"pool/store.go": strings.Replace(
									readRepoFileHelper(t, opsSrv),
									"if err := rows.ValidateChecksum(); err != nil {",
									"defer rows.Close()\n\tif err := rows.ValidateChecksum(); err != nil {",
									1,
								),
							},
						},
					}},
				}
			default:
				parts = []map[string]any{
					{"text": "Diagnosed INC-402: config/pool.yaml capped max_open_conns at 2 with idle_timeout_seconds=0, and pool/store.go leaked rows on checksum errors. Staged verified fix on branch fix/inc-402-pool."},
				}
			}

			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []map[string]any{
					{
						"content": map[string]any{
							"role":  "model",
							"parts": parts,
						},
						"finishReason": "STOP",
					},
				},
			})
			return
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakeBroker.Close)

	wsDir := t.TempDir()
	outsideFile := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("top-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(outsideFile, filepath.Join(wsDir, "escape-link"))

	srv, err := agentruntime.NewServer(agentruntime.Config{
		WorkspaceDir: wsDir,
		BrokerURL:    fakeBroker.URL,
		ModelName:    "gemini-3.8-flash",
		TemplateName: "sre-agent-v1",
		BuildRunner: func(ctx context.Context, cfg agentruntime.Config, tools []tool.Tool) (*runner.Runner, error) {
			llm, err := brokerclient.NewModel(ctx, cfg.BrokerURL, cfg.ModelName, fakeBroker.Client())
			if err != nil {
				return nil, err
			}
			a, err := llmagent.New(llmagent.Config{
				Name:  "sre_triage_agent",
				Model: llm,
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
				Agent:             a,
				SessionService:    session.InMemoryService(),
				AutoCreateSession: true,
			})
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	actorSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(actorSrv.Close)

	reqBody, _ := json.Marshal(agentruntime.TurnRequest{
		IncidentID: "INC-402",
		SessionID:  "sess-402",
		UserID:     "sre-operator",
		Prompt:     "Investigate INC-402 and stage a verified patch",
	})
	resp, err := http.Post(actorSrv.URL+"/turn", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /turn: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /turn status = %d, body = %s", resp.StatusCode, string(raw))
	}

	var turnResp agentruntime.TurnResponse
	if err := json.NewDecoder(resp.Body).Decode(&turnResp); err != nil {
		t.Fatalf("decode TurnResponse: %v", err)
	}
	if !strings.Contains(turnResp.Reply, "INC-402") {
		t.Errorf("Reply = %q, want diagnosis mentioning INC-402", turnResp.Reply)
	}
	if len(turnResp.ToolSteps) < 6 {
		t.Fatalf("ToolSteps count = %d, want >= 6 (%+v)", len(turnResp.ToolSteps), turnResp.ToolSteps)
	}

	// Verify ToolSteps records structured rejection messages for path traversal and disallowed commands.
	var sawTraversalReject, sawCmdReject, sawStageOK bool
	for _, st := range turnResp.ToolSteps {
		if st.Name == "read_workspace_file" && strings.Contains(st.Result, "escapes workspace") {
			sawTraversalReject = true
		}
		if st.Name == "run_workspace_command" && strings.Contains(st.Result, "not allowlisted") {
			sawCmdReject = true
		}
		if st.Name == "stage_git_patch" && strings.Contains(st.Result, "fix/inc-402-pool") {
			sawStageOK = true
		}
	}
	if !sawTraversalReject {
		t.Errorf("expected path traversal rejection in ToolSteps, got %+v", turnResp.ToolSteps)
	}
	if !sawCmdReject {
		t.Errorf("expected command allowlist rejection in ToolSteps, got %+v", turnResp.ToolSteps)
	}
	if !sawStageOK {
		t.Errorf("expected stage_git_patch success in ToolSteps, got %+v", turnResp.ToolSteps)
	}

	// Verify zero Authorization headers left the keyless actor.
	mu.Lock()
	gotAuth := append([]string(nil), authHdrs...)
	mu.Unlock()
	for _, h := range gotAuth {
		if h != "" {
			t.Errorf("keyless actor sent unexpected Authorization header %q", h)
		}
	}

	// Verify GET /workspace/state reflects the cloned and patched branch.
	wsResp, err := http.Get(actorSrv.URL + "/workspace/state")
	if err != nil {
		t.Fatalf("GET /workspace/state: %v", err)
	}
	defer wsResp.Body.Close()
	var wsState agentruntime.WorkspaceState
	if err := json.NewDecoder(wsResp.Body).Decode(&wsState); err != nil {
		t.Fatalf("decode WorkspaceState: %v", err)
	}
	if !wsState.Cloned || wsState.Branch != "fix/inc-402-pool" || wsState.VerifyOK != true {
		t.Errorf("WorkspaceState = %+v, want Cloned=true Branch=fix/inc-402-pool VerifyOK=true", wsState)
	}
}

func readRepoFileHelper(t *testing.T, _ *opshub.Server) string {
	t.Helper()
	tmp := t.TempDir()
	// Seed a clean copy of the initial repo files to construct the patched store.go fixture.
	if err := agentruntime.SeedInitialRepoForTest(tmp); err != nil {
		t.Fatalf("SeedInitialRepoForTest: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(tmp, "pool", "store.go"))
	if err != nil {
		t.Fatalf("read seeded store.go: %v", err)
	}
	return string(b)
}

func TestGoldenSnapshotWarmingRequiresZeroEgressBeforeFirstTurn(t *testing.T) {
	var egressAllowed atomic.Bool
	var preWakeHits atomic.Int32

	gatedBroker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !egressAllowed.Load() {
			preWakeHits.Add(1)
			http.Error(w, "default-deny egress during Golden Snapshot warming", http.StatusForbidden)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/agentregistry/v1/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"mcpServers": []any{}})
		case strings.HasPrefix(r.URL.Path, "/otlp/"):
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/genai/"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []map[string]any{
					{
						"content": map[string]any{
							"role":  "model",
							"parts": []map[string]any{{"text": "INC-402 ready after lazy runner init."}},
						},
						"finishReason": "STOP",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gatedBroker.Close)

	srv, err := agentruntime.NewServer(agentruntime.Config{
		WorkspaceDir: t.TempDir(),
		BrokerURL:    gatedBroker.URL,
		ModelName:    "gemini-3.8-flash",
		TemplateName: "sre-agent-v4",
		BuildRunner: func(ctx context.Context, cfg agentruntime.Config, tools []tool.Tool) (*runner.Runner, error) {
			regClient, err := brokerclient.NewAgentRegistryClient(ctx, cfg.BrokerURL, "demo-proj", "us-central1", gatedBroker.Client())
			if err != nil {
				return nil, err
			}
			mcpToolsets, err := brokerclient.DiscoverMCPToolsets(ctx, regClient, cfg.BrokerURL, gatedBroker.Client())
			if err != nil {
				return nil, err
			}
			llm, err := brokerclient.NewModel(ctx, cfg.BrokerURL, cfg.ModelName, gatedBroker.Client())
			if err != nil {
				return nil, err
			}
			a, err := llmagent.New(llmagent.Config{
				Name:     "sre_triage_agent",
				Model:    llm,
				Tools:    tools,
				Toolsets: mcpToolsets,
			})
			if err != nil {
				return nil, err
			}
			return runner.New(runner.Config{
				AppName:           agentruntime.AppName,
				Agent:             a,
				SessionService:    session.InMemoryService(),
				AutoCreateSession: true,
			})
		},
	})
	if err != nil {
		t.Fatalf("NewServer failed while egress was blocked: %v", err)
	}
	actorTS := httptest.NewServer(srv.Handler())
	t.Cleanup(actorTS.Close)

	readyResp, err := http.Get(actorTS.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	readyResp.Body.Close()
	if readyResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /readyz = %d, want 200", readyResp.StatusCode)
	}
	if hits := preWakeHits.Load(); hits != 0 {
		t.Fatalf("expected 0 outbound broker requests during Golden Snapshot /readyz warming, got %d", hits)
	}

	// Simulate EgressPolicy activation once cloned into an Incident Actor.
	egressAllowed.Store(true)
	body, _ := json.Marshal(agentruntime.TurnRequest{
		IncidentID: "INC-402",
		SessionID:  "inc-402",
		UserID:     "sre-operator",
		Prompt:     "Status check",
	})
	turnResp, err := http.Post(actorTS.URL+"/turn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /turn: %v", err)
	}
	defer turnResp.Body.Close()
	if turnResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(turnResp.Body)
		t.Fatalf("POST /turn status = %d, body = %s", turnResp.StatusCode, string(raw))
	}
}

func TestStagePatchRecoversFromStaleGitIndexLock(t *testing.T) {
	opsSrv, err := opshub.NewServer(opshub.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opshub.NewServer: %v", err)
	}
	opsHandler := opsSrv.Handler()
	fakeBroker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ops/") {
			r2 := r.Clone(r.Context())
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, "/ops")
			opsHandler.ServeHTTP(w, r2)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(fakeBroker.Close)

	wsDir := t.TempDir()
	ws, err := agentruntime.NewWorkspace(wsDir, fakeBroker.URL)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.CloneRepo(context.Background(), agentruntime.CloneRepoArgs{Repo: "ledger-api"}); err != nil {
		t.Fatalf("CloneRepo: %v", err)
	}

	// Simulate stale git lock files left behind when an earlier HTTP turn was canceled mid-commit.
	gitDir := filepath.Join(ws.RootDir(), "ledger-api", ".git")
	if err := os.WriteFile(filepath.Join(gitDir, "index.lock"), []byte("stale"), 0o600); err != nil {
		t.Fatalf("write index.lock: %v", err)
	}
	refLockDir := filepath.Join(gitDir, "refs", "heads", "fix")
	if err := os.MkdirAll(refLockDir, 0o755); err != nil {
		t.Fatalf("mkdir refLockDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(refLockDir, "inc-402-pool-leak.lock"), []byte("stale"), 0o600); err != nil {
		t.Fatalf("write ref lock: %v", err)
	}

	res, err := ws.StagePatch(context.Background(), agentruntime.StagePatchArgs{
		Branch:        "fix/inc-402-pool-leak",
		CommitMessage: "Fix pool sizing and row cursor release",
		Files: map[string]string{
			"config/pool.yaml": "max_open_conns: 64\nidle_timeout_seconds: 30\nacquire_timeout_ms: 150\n",
			"pool/store.go": strings.Replace(
				readRepoFileHelper(t, opsSrv),
				"if err := rows.ValidateChecksum(); err != nil {",
				"defer rows.Close()\n\tif err := rows.ValidateChecksum(); err != nil {",
				1,
			),
		},
	})
	if err != nil {
		t.Fatalf("StagePatch error: %v", err)
	}
	if !res.Staged || !res.VerifyOK || res.Error != "" {
		t.Fatalf("StagePatch failed due to stale git lock: %+v", res)
	}
}

func TestTurnResponseAlwaysSerializesToolStepsArrayWhenZeroToolsRun(t *testing.T) {
	fakeBroker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/genai/"):
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Recalled INC-402 root cause via preload_memory."}]}}]}`))
		case strings.HasPrefix(r.URL.Path, "/otlp/"):
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakeBroker.Close)

	tests := []struct {
		name        string
		template    string
		buildRunner func(context.Context, agentruntime.Config, []tool.Tool) (*runner.Runner, error)
	}{{
		name:     "starter stub with nil runner",
		template: "sre-agent-v1",
		buildRunner: func(context.Context, agentruntime.Config, []tool.Tool) (*runner.Runner, error) {
			return nil, nil
		},
	}, {
		name:     "ADK runner turn with zero runtime tool calls",
		template: "sre-agent-v2",
		buildRunner: func(ctx context.Context, cfg agentruntime.Config, wsTools []tool.Tool) (*runner.Runner, error) {
			llm, err := brokerclient.NewModel(ctx, cfg.BrokerURL, cfg.ModelName, fakeBroker.Client())
			if err != nil {
				return nil, err
			}
			ag, err := llmagent.New(llmagent.Config{
				Name:  "sre_triage_agent",
				Model: llm,
				Tools: wsTools,
			})
			if err != nil {
				return nil, err
			}
			return runner.New(runner.Config{
				AppName:           agentruntime.AppName,
				Agent:             ag,
				SessionService:    session.InMemoryService(),
				AutoCreateSession: true,
			})
		},
	}}

	reqBytes, _ := json.Marshal(agentruntime.TurnRequest{
		IncidentID: "INC-403",
		SessionID:  "inc-403",
		UserID:     "sre-operator",
		Prompt:     "Triage INC-403 from memory",
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, err := agentruntime.NewServer(agentruntime.Config{
				WorkspaceDir: t.TempDir(),
				BrokerURL:    fakeBroker.URL,
				ModelName:    "gemini-3.8-flash",
				TemplateName: tt.template,
				BuildRunner:  tt.buildRunner,
			})
			if err != nil {
				t.Fatalf("NewServer: %v", err)
			}
			ts := httptest.NewServer(srv.Handler())
			t.Cleanup(ts.Close)

			resp, err := http.Post(ts.URL+"/turn", "application/json", bytes.NewReader(reqBytes))
			if err != nil {
				t.Fatalf("POST /turn: %v", err)
			}
			var fields map[string]json.RawMessage
			decErr := json.NewDecoder(resp.Body).Decode(&fields)
			_ = resp.Body.Close()
			if decErr != nil {
				t.Fatalf("decode JSON map: %v", decErr)
			}
			rawSteps, present := fields["tool_steps"]
			if !present || strings.TrimSpace(string(rawSteps)) != "[]" {
				t.Fatalf("tool_steps JSON = %q (present=%t), want \"[]\"", string(rawSteps), present)
			}
		})
	}
}
