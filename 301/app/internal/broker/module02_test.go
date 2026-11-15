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

package broker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gke-demos/substrate-agent-platform/app/internal/agentruntime"
	"github.com/gke-demos/substrate-agent-platform/app/internal/ateclient"
	"github.com/gke-demos/substrate-agent-platform/app/internal/ateclient/atetest"
	"github.com/gke-demos/substrate-agent-platform/app/internal/broker"
	"github.com/gke-demos/substrate-agent-platform/app/internal/brokerclient"
	"github.com/gke-demos/substrate-agent-platform/app/internal/ledgersvc"
	"github.com/gke-demos/substrate-agent-platform/app/internal/opshub"
	ateapipb "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/loadmemorytool"
	"google.golang.org/adk/v2/tool/preloadmemorytool"
)

func buildModule02Runner(ctx context.Context, cfg agentruntime.Config, wsTools []tool.Tool) (*runner.Runner, error) {
	llm, err := brokerclient.NewModel(ctx, cfg.BrokerURL, cfg.ModelName, nil)
	if err != nil {
		return nil, err
	}
	sessionSvc, err := brokerclient.NewSessionService(cfg.BrokerURL, nil)
	if err != nil {
		return nil, err
	}
	memorySvc, err := brokerclient.NewMemoryService(cfg.BrokerURL, nil)
	if err != nil {
		return nil, err
	}
	memPlugin, err := brokerclient.NewMemoryConsolidationPlugin(sessionSvc, memorySvc)
	if err != nil {
		return nil, err
	}

	tools := append([]tool.Tool(nil), wsTools...)
	tools = append(tools, loadmemorytool.New(), preloadmemorytool.New())

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
		SessionService:    sessionSvc,
		MemoryService:     memorySvc,
		PluginConfig:      runner.PluginConfig{Plugins: []*plugin.Plugin{memPlugin}},
		AutoCreateSession: true,
	})
}

func TestModule02ZeroWakeSessionLedgerRolloutAndSiblingMemoryRecall(t *testing.T) {
	ledger := ledgersvc.NewServer(ledgersvc.Options{
		Config:    ledgersvc.RuntimeConfig{MaxOpenConns: 2, IdleTimeoutSeconds: 0, AcquireTimeoutMs: 60},
		LogOutput: io.Discard,
	})
	t.Cleanup(ledger.Close)
	_ = ledger.ExecuteTransfer(context.Background(), "bad-1")
	_ = ledger.ExecuteTransfer(context.Background(), "bad-2")
	ledgerTS := httptest.NewServer(ledger.Handler())
	t.Cleanup(ledgerTS.Close)

	ops, err := opshub.NewServer(opshub.Options{
		DataDir:      t.TempDir(),
		SimNamespace: "prod-sim",
		LedgerAPIURL: ledgerTS.URL,
	})
	if err != nil {
		t.Fatalf("opshub.NewServer: %v", err)
	}
	opsTS := httptest.NewServer(ops.Handler())
	t.Cleanup(opsTS.Close)

	fakeATE := atetest.NewFakeControl()
	ateAddr, caFile, tokenFile := atetest.StartServer(t, fakeATE)

	gwLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen broker: %v", err)
	}
	brokerURL := "http://" + gwLis.Addr().String()

	// Create per-(actor, template) isolated sandbox instances so rolling out a new
	// template (sre-agent-v1 -> sre-agent-v2) or spawning sibling actor inc-403
	// starts with a clean /workspace, modeling real Golden Snapshot behavior.
	var (
		actorMu      sync.Mutex
		actorServers = make(map[string]*httptest.Server)
	)
	getOrStartActorInstance := func(actorName, tmplName string) *httptest.Server {
		key := actorName + "@" + tmplName
		actorMu.Lock()
		defer actorMu.Unlock()
		if ts, ok := actorServers[key]; ok {
			return ts
		}
		buildFn := buildModule02Runner
		if tmplName == "sre-agent-v1" {
			buildFn = func(ctx context.Context, cfg agentruntime.Config, wsTools []tool.Tool) (*runner.Runner, error) {
				llm, err := brokerclient.NewModel(ctx, cfg.BrokerURL, cfg.ModelName, nil)
				if err != nil {
					return nil, err
				}
				a, err := llmagent.New(llmagent.Config{
					Name:  "sre_triage_agent",
					Model: llm,
					InstructionProvider: func(agent.ReadonlyContext) (string, error) {
						return agentruntime.DefaultInstruction, nil
					},
					Tools: wsTools,
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
			}
		}
		srv, err := agentruntime.NewServer(agentruntime.Config{
			WorkspaceDir: t.TempDir(),
			BrokerURL:    brokerURL,
			ModelName:    "gemini-3.8-flash",
			TemplateName: tmplName,
			BuildRunner:  buildFn,
		})
		if err != nil {
			t.Fatalf("agentruntime.NewServer(%s): %v", key, err)
		}
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(ts.Close)
		actorServers[key] = ts
		return ts
	}

	var routerCalls atomic.Int64
	routerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routerCalls.Add(1)
		target := r.Header.Get("ate-target-actor") // "<atespace>/<actor>"
		parts := strings.SplitN(target, "/", 2)
		if len(parts) != 2 {
			http.Error(w, "invalid target actor header", http.StatusBadRequest)
			return
		}
		atespace, actorName := parts[0], parts[1]
		actorObj := fakeATE.Actor(atespace, actorName)
		tmplName := "sre-agent-v2"
		if actorObj != nil && actorObj.GetActorTemplate().GetName() != "" {
			tmplName = actorObj.GetActorTemplate().GetName()
		}
		fakeATE.MarkRunning(atespace, actorName, "sre-workers-0")

		targetTS := getOrStartActorInstance(actorName, tmplName)
		fwdReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetTS.URL+r.URL.RequestURI(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fwdReq.Header = r.Header.Clone()
		resp, err := targetTS.Client().Do(fwdReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(routerTS.Close)

	gwSrv, err := broker.NewServer(broker.Config{
		Namespace:       "workshop-301",
		Atespace:        "workshop-301",
		SimNamespace:    "prod-sim",
		ProjectID:       "demo-project",
		Region:          "us-central1",
		ModelLocation:   "global",
		GeminiModel:     "gemini-3.8-flash",
		MockLLM:         true,
		DefaultTemplate: "sre-agent-v1",
		SelfURL:         brokerURL,
		RouterURL:       routerTS.URL,
		OpsHubURL:       opsTS.URL,
		LedgerAPIURL:    ledgerTS.URL,
		IdleAfter:       35 * time.Millisecond,
		SPIFFEMode:      "wif",
		SPIFFEDomain:    "demo-project.svc.id.goog",
		ATE: ateclient.Options{
			Addr:      ateAddr,
			CAFile:    caFile,
			TokenFile: tokenFile,
		},
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = gwSrv.Close() })

	httpSrv := &http.Server{Handler: gwSrv.Handler()}
	go func() { _ = httpSrv.Serve(gwLis) }()
	t.Cleanup(func() { _ = httpSrv.Close() })

	postChat := func(req broker.ChatRequest) broker.ChatResponse {
		t.Helper()
		b, _ := json.Marshal(req)
		resp, err := http.Post(brokerURL+"/api/chat", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("POST /api/chat: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("POST /api/chat status = %d: %s", resp.StatusCode, string(raw))
		}
		var out broker.ChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode ChatResponse: %v", err)
		}
		return out
	}

	// Step 1: Run v1 turn on INC-402 (clones /workspace inside sre-agent-v1, in-memory session only).
	v1Turn := postChat(broker.ChatRequest{
		IncidentID: "INC-402",
		Template:   "sre-agent-v1",
		Prompt:     "Investigate INC-402 by cloning ledger-api and running ledger-verify.",
	})
	if v1Turn.Template != "sre-agent-v1" || !v1Turn.Workspace.Cloned {
		t.Fatalf("v1Turn = %+v, want sre-agent-v1 with Cloned=true", v1Turn)
	}

	// Step 2: Roll out sre-agent-v2 on INC-402. EnsureActor replaces the v1 actor with a fresh v2 actor
	// (proving raw v1 snapshots do not carry across image upgrades), and Turn 1 on v2 persists to
	// Session Ledger + Consolidated Memory.
	v2Investigate := postChat(broker.ChatRequest{
		IncidentID: "INC-402",
		Template:   "sre-agent-v2",
		Prompt:     "Investigate INC-402 by cloning ledger-api into /workspace, inspecting config/pool.yaml and pool/store.go, and running ledger-verify.",
	})
	if v2Investigate.Template != "sre-agent-v2" || !v2Investigate.Workspace.Cloned {
		t.Fatalf("v2Investigate = %+v, want sre-agent-v2 with Cloned=true", v2Investigate)
	}

	// Step 3: Let idle-suspender transition inc-402 to SUSPENDED (0 vCPU), then inspect Session Ledger
	// and Consolidated Memory with ZERO router wakeups!
	time.Sleep(55 * time.Millisecond)
	if err := gwSrv.RunIdleTick(context.Background()); err != nil {
		t.Fatalf("RunIdleTick: %v", err)
	}
	if a := fakeATE.Actor("workshop-301", "inc-402"); a == nil || a.GetStatus().GetState() != ateapipb.ActorState_ACTOR_STATE_SUSPENDED {
		t.Fatalf("inc-402 state = %v, want ACTOR_STATE_SUSPENDED", a)
	}

	callsBeforeZeroWake := routerCalls.Load()

	sessDetailResp, err := http.Get(brokerURL + "/api/ledger/sessions/inc-402")
	if err != nil {
		t.Fatalf("GET /api/ledger/sessions/inc-402: %v", err)
	}
	defer sessDetailResp.Body.Close()
	if sessDetailResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/ledger/sessions/inc-402 status = %d, want 200", sessDetailResp.StatusCode)
	}
	var detail broker.LedgerSessionDetail
	if err := json.NewDecoder(sessDetailResp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode LedgerSessionDetail: %v", err)
	}
	if detail.ActorState != "SUSPENDED" || !strings.Contains(detail.ComputeBadge, "0 vCPU") {
		t.Errorf("zero-wake session detail actor state = %q (%q), want SUSPENDED with 0 vCPU badge", detail.ActorState, detail.ComputeBadge)
	}
	if len(detail.Events) < 4 {
		t.Errorf("Session Ledger events count = %d, want >= 4 (prompt + tool calls + replies)", len(detail.Events))
	}

	memResp, err := http.Get(brokerURL + "/api/ledger/memories?q=MAX_OPEN_CONNS")
	if err != nil {
		t.Fatalf("GET /api/ledger/memories: %v", err)
	}
	defer memResp.Body.Close()
	var memPayload struct {
		Memories []broker.ConsolidatedMemoryFact `json:"memories"`
	}
	if err := json.NewDecoder(memResp.Body).Decode(&memPayload); err != nil {
		t.Fatalf("decode memories: %v", err)
	}
	if len(memPayload.Memories) == 0 {
		t.Fatalf("expected Consolidated Memory facts extracted from INC-402, got 0")
	}

	if routerCalls.Load() != callsBeforeZeroWake {
		t.Fatalf("zero-wake Session Ledger / Memory endpoints dialed atenet-router (%d -> %d)", callsBeforeZeroWake, routerCalls.Load())
	}

	// Step 4: Simulate deleting/recreating inc-402 on v2 (or rolling out a fresh v2 instance) AND
	// spawning sibling actor inc-403 (INC-403). Verify raw workspace is uncloned while Consolidated
	// Memory recalls INC-402 root-cause and pool-tuning facts via load_memory!
	_, _ = fakeATE.DeleteActor(context.Background(), &ateapipb.DeleteActorRequest{
		Actor: &ateapipb.ObjectRef{Atespace: "workshop-301", Name: "inc-402"},
	})
	actorMu.Lock()
	delete(actorServers, "inc-402@sre-agent-v2")
	actorMu.Unlock()

	upgradedRecall := postChat(broker.ChatRequest{
		IncidentID: "INC-402",
		Template:   "sre-agent-v2",
		Prompt:     "Summarize the previously verified INC-402 connection pool root cause and tuning values from memory without re-cloning.",
	})
	if upgradedRecall.Workspace.Cloned {
		t.Errorf("expected fresh v2 rollout workspace to start uncloned (raw snapshot invalidated), got Cloned=true")
	}
	for _, want := range []string{"INC-402", "MAX_OPEN_CONNS=2", "IDLE_TIMEOUT_SECONDS=0"} {
		if !strings.Contains(upgradedRecall.Reply, want) {
			t.Errorf("upgraded v2 actor reply missing recalled %q:\n%s", want, upgradedRecall.Reply)
		}
	}

	siblingTurn := postChat(broker.ChatRequest{
		IncidentID: "INC-403",
		Template:   "sre-agent-v2",
		Prompt:     "Triage sibling incident INC-403 using Consolidated Memory from prior ledger-api incidents.",
	})
	if siblingTurn.ActorName != "inc-403" {
		t.Errorf("sibling ActorName = %q, want inc-403", siblingTurn.ActorName)
	}
	var sawLoadMemory bool
	for _, st := range siblingTurn.ToolSteps {
		if st.Name == "load_memory" {
			sawLoadMemory = true
		}
	}
	if !sawLoadMemory {
		t.Errorf("expected sibling inc-403 ToolSteps to include load_memory, got %+v", siblingTurn.ToolSteps)
	}
	for _, want := range []string{"INC-402", "MAX_OPEN_CONNS=2", "IDLE_TIMEOUT_SECONDS=0"} {
		if !strings.Contains(siblingTurn.Reply, want) {
			t.Errorf("sibling inc-403 reply missing recalled %q:\n%s", want, siblingTurn.Reply)
		}
	}

	// Step 5: Verify Incident Console /sessions view exposes Session ledger + Consolidated memory UI.
	sessionsHTMLResp, err := http.Get(brokerURL + "/sessions")
	if err != nil {
		t.Fatalf("GET /sessions: %v", err)
	}
	sessionsHTMLBytes, _ := io.ReadAll(sessionsHTMLResp.Body)
	sessionsHTMLResp.Body.Close()
	sessionsHTML := string(sessionsHTMLBytes)
	for _, marker := range []string{"Session ledger", "Consolidated memory", "memory-search-input", "session-events-inspector"} {
		if !strings.Contains(sessionsHTML, marker) {
			t.Errorf("/sessions HTML missing %q", marker)
		}
	}
}
