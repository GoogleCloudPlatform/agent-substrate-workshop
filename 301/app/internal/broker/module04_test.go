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
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/agentruntime"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ateclient"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ateclient/atetest"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/broker"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/brokerclient"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ledgersvc"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/opshub"
	ateapipb "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/loadmemorytool"
	"google.golang.org/adk/v2/tool/preloadmemorytool"
	"google.golang.org/adk/v2/tool/skilltoolset"
)

func TestModule04A2AAgentCardAndJSONRPCTriage(t *testing.T) {
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

	actorSrv, err := agentruntime.NewServer(agentruntime.Config{
		WorkspaceDir: t.TempDir(),
		BrokerURL:    brokerURL,
		ModelName:    "gemini-3.8-flash",
		TemplateName: "sre-agent-v3",
		BuildRunner:  buildModule03Runner,
	})
	if err != nil {
		t.Fatalf("agentruntime.NewServer: %v", err)
	}
	actorTS := httptest.NewServer(actorSrv.Handler())
	t.Cleanup(actorTS.Close)

	routerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fakeATE.MarkRunning("workshop-301", "inc-402", "sre-workers-0")
		fwdReq, err := http.NewRequestWithContext(r.Context(), r.Method, actorTS.URL+r.URL.RequestURI(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fwdReq.Header = r.Header.Clone()
		resp, err := actorTS.Client().Do(fwdReq)
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
		DefaultTemplate: "sre-agent-v3",
		SelfURL:         brokerURL,
		RouterURL:       routerTS.URL,
		OpsHubURL:       opsTS.URL,
		LedgerAPIURL:    ledgerTS.URL,
		IdleAfter:       15 * time.Second,
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

	// 1. Verify GET /.well-known/agent-card.json serves a valid A2A Agent Card.
	cardResp, err := http.Get(brokerURL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("GET /.well-known/agent-card.json: %v", err)
	}
	defer cardResp.Body.Close()
	if cardResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /.well-known/agent-card.json status = %d, want 200", cardResp.StatusCode)
	}
	var card struct {
		Name                string `json:"name"`
		Description         string `json:"description"`
		Version             string `json:"version"`
		URL                 string `json:"url"`
		ProtocolVersion     string `json:"protocolVersion"`
		SupportedInterfaces []struct {
			URL             string `json:"url"`
			ProtocolBinding string `json:"protocolBinding"`
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"supportedInterfaces"`
		Capabilities struct {
			Streaming              bool `json:"streaming"`
			StateTransitionHistory bool `json:"stateTransitionHistory"`
		} `json:"capabilities"`
		Skills []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(cardResp.Body).Decode(&card); err != nil {
		t.Fatalf("decode agent-card.json: %v", err)
	}
	if card.Name != "Platform SRE Triage Agent" {
		t.Errorf("AgentCard.Name = %q, want %q", card.Name, "Platform SRE Triage Agent")
	}
	if card.Version == "" {
		t.Errorf("AgentCard.Version is empty")
	}
	if card.URL != brokerURL+"/a2a" {
		t.Errorf("AgentCard.URL = %q, want %q", card.URL, brokerURL+"/a2a")
	}
	if len(card.SupportedInterfaces) == 0 || card.SupportedInterfaces[0].URL != brokerURL+"/a2a" || card.SupportedInterfaces[0].ProtocolBinding != "JSONRPC" {
		t.Errorf("AgentCard.SupportedInterfaces = %+v, want JSONRPC interface at %s/a2a", card.SupportedInterfaces, brokerURL)
	}
	if len(card.Skills) < 2 {
		t.Errorf("AgentCard.Skills len = %d, want >= 2", len(card.Skills))
	}

	// 2. Verify POST /a2a JSON-RPC 2.0 message/send delegates into governed triage pipeline.
	sendReqBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "rpc-1",
		"method":  "message/send",
		"params": map[string]any{
			"contextId": "inc-402",
			"metadata": map[string]any{
				"incident_id": "INC-402",
			},
			"message": map[string]any{
				"role": "user",
				"parts": []map[string]any{
					{"kind": "text", "text": "Investigate INC-402 by cloning ledger-api into /workspace and running ledger-verify."},
				},
			},
		},
	})
	sendResp, err := http.Post(brokerURL+"/a2a", "application/json", bytes.NewReader(sendReqBody))
	if err != nil {
		t.Fatalf("POST /a2a message/send: %v", err)
	}
	defer sendResp.Body.Close()
	if sendResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(sendResp.Body)
		t.Fatalf("POST /a2a status = %d: %s", sendResp.StatusCode, string(raw))
	}
	var sendRPC struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Result  struct {
			ID        string `json:"id"`
			ContextID string `json:"contextId"`
			Status    struct {
				State string `json:"state"`
			} `json:"status"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"result"`
	}
	if err := json.NewDecoder(sendResp.Body).Decode(&sendRPC); err != nil {
		t.Fatalf("decode /a2a message/send response: %v", err)
	}
	if sendRPC.JSONRPC != "2.0" || sendRPC.ID != "rpc-1" {
		t.Errorf("JSON-RPC envelope = %+v, want jsonrpc=2.0 id=rpc-1", sendRPC)
	}
	if sendRPC.Result.ID == "" || sendRPC.Result.Status.State != "completed" {
		t.Fatalf("Task result = %+v, want non-empty id and state=completed", sendRPC.Result)
	}
	if len(sendRPC.Result.Parts) == 0 || !strings.Contains(sendRPC.Result.Parts[0].Text, "MAX_OPEN_CONNS=2") {
		t.Errorf("A2A reply parts = %+v, want grounded diagnosis mentioning MAX_OPEN_CONNS=2", sendRPC.Result.Parts)
	}

	// 3. Verify POST /a2a JSON-RPC 2.0 tasks/get returns the persisted task.
	getTaskBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "rpc-2",
		"method":  "tasks/get",
		"params": map[string]any{
			"id": sendRPC.Result.ID,
		},
	})
	getResp, err := http.Post(brokerURL+"/a2a", "application/json", bytes.NewReader(getTaskBody))
	if err != nil {
		t.Fatalf("POST /a2a tasks/get: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /a2a tasks/get status = %d, want 200", getResp.StatusCode)
	}
	var getRPC struct {
		Result struct {
			ID     string `json:"id"`
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"result"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&getRPC); err != nil {
		t.Fatalf("decode tasks/get response: %v", err)
	}
	if getRPC.Result.ID != sendRPC.Result.ID || getRPC.Result.Status.State != "completed" {
		t.Errorf("tasks/get result = %+v, want id=%q state=completed", getRPC.Result, sendRPC.Result.ID)
	}
}

func TestModule04AgentRegistryCloudMergeAndDestructiveTrap403(t *testing.T) {
	cloudRegistryTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/projects/demo-project/locations/us-central1/mcpServers":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mcpServers": []map[string]any{
					{
						"name":        "projects/demo-project/locations/us-central1/mcpServers/cloud-custom-mcp",
						"mcpServerId": "cloud-custom-mcp",
						"displayName": "Cloud Custom Telemetry MCP",
						"description": "Synced from live Cloud Agent Registry",
						"interfaces": []map[string]any{
							{"url": "https://telemetry.googleapis.com/mcp", "protocolBinding": "JSONRPC"},
						},
						"tools": []map[string]any{
							{
								"name":        "list_slo_burn_alerts",
								"description": "Read SLO burn alerts",
								"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false},
							},
						},
					},
				},
			})
		case "/v1/projects/demo-project/locations/us-central1/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"agents": []map[string]any{
					{
						"name":        "projects/demo-project/locations/us-central1/agents/cloud-peer-auditor",
						"displayName": "Cloud Peer Security Auditor",
						"description": "Synced from live Cloud Agent Registry",
						"version":     "1.0.0",
						"protocols": []map[string]any{
							{
								"type":            "A2A_AGENT",
								"protocolVersion": "0.3.0",
								"interfaces": []map[string]any{
									{"url": "https://auditor.example.internal/a2a", "protocolBinding": "JSONRPC"},
								},
							},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cloudRegistryTS.Close)

	genaiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(genaiStub.Close)

	ledger := ledgersvc.NewServer(ledgersvc.Options{
		Config:    ledgersvc.RuntimeConfig{MaxOpenConns: 2, IdleTimeoutSeconds: 0, AcquireTimeoutMs: 60},
		LogOutput: io.Discard,
	})
	t.Cleanup(ledger.Close)
	ledgerTS := httptest.NewServer(ledger.Handler())
	t.Cleanup(ledgerTS.Close)

	ops, err := opshub.NewServer(opshub.Options{
		DataDir:      t.TempDir(),
		SimNamespace: "prod-sim",
		LedgerAPIURL: ledgerTS.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	opsTS := httptest.NewServer(ops.Handler())
	t.Cleanup(opsTS.Close)

	gwSrv, err := broker.NewServer(broker.Config{
		Namespace:                "workshop-301",
		Atespace:                 "workshop-301",
		SimNamespace:             "prod-sim",
		ProjectID:                "demo-project",
		Region:                   "us-central1",
		MockLLM:                  false,
		GenAIUpstreamURL:         genaiStub.URL,
		SkillRegistryUpstreamURL: cloudRegistryTS.URL,
		AgentRegistryUpstreamURL: cloudRegistryTS.URL,
		CloudTraceUpstreamURL:    genaiStub.URL,
		OpsHubURL:                opsTS.URL,
		LedgerAPIURL:             ledgerTS.URL,
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = gwSrv.Close() })
	gwTS := httptest.NewServer(gwSrv.Handler())
	t.Cleanup(gwTS.Close)

	// Verify /agentregistry/v1/.../mcpServers merges live Cloud entries with governed servers.
	mcpListResp, err := http.Get(gwTS.URL + "/agentregistry/v1/projects/demo-project/locations/us-central1/mcpServers")
	if err != nil {
		t.Fatalf("GET mcpServers: %v", err)
	}
	var mcpDoc struct {
		MCPServers []struct {
			MCPServerID string `json:"mcpServerId"`
			Interfaces  []struct {
				URL string `json:"url"`
			} `json:"interfaces"`
		} `json:"mcpServers"`
	}
	_ = json.NewDecoder(mcpListResp.Body).Decode(&mcpDoc)
	mcpListResp.Body.Close()

	seenServers := map[string]string{}
	for _, s := range mcpDoc.MCPServers {
		if len(s.Interfaces) > 0 {
			seenServers[s.MCPServerID] = s.Interfaces[0].URL
		}
	}
	for _, want := range []string{"logging-mcp", "container-mcp", "ops-hub-mcp", "cloud-custom-mcp"} {
		if seenServers[want] == "" {
			t.Errorf("merged mcpServers missing %q (got %v)", want, seenServers)
		}
	}
	if got := seenServers["logging-mcp"]; got != gwTS.URL+"/mcp/logging-mcp" {
		t.Errorf("logging-mcp interface URL = %q, want %s/mcp/logging-mcp", got, gwTS.URL)
	}

	// Verify /mcp/container-mcp blocks destructive delete_production_namespace with HTTP 403.
	trapCall, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "delete_production_namespace",
			"arguments": map[string]any{
				"namespace": "prod-sim",
				"reason":    "unauthorized test",
			},
		},
	})
	trapResp, err := http.Post(gwTS.URL+"/mcp/container-mcp", "application/json", bytes.NewReader(trapCall))
	if err != nil {
		t.Fatalf("POST /mcp/container-mcp: %v", err)
	}
	trapResp.Body.Close()
	if trapResp.StatusCode != http.StatusForbidden {
		t.Errorf("delete_production_namespace status = %d, want 403 Forbidden", trapResp.StatusCode)
	}
}

func buildModule04Runner(ctx context.Context, cfg agentruntime.Config, wsTools []tool.Tool) (*runner.Runner, error) {
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

	remediationTools, err := brokerclient.NewRemediationTools(cfg.BrokerURL, cfg.WorkspaceDir, nil)
	if err != nil {
		return nil, err
	}
	tools = append(tools, remediationTools...)

	skillSource, err := brokerclient.NewSkillSource(cfg.BrokerURL, cfg.WorkspaceDir, nil)
	if err != nil {
		return nil, err
	}
	skillsTS, err := skilltoolset.New(ctx, skilltoolset.Config{
		Source: skillSource,
	})
	if err != nil {
		return nil, err
	}
	toolsets := []tool.Toolset{skillsTS}

	regClient, err := brokerclient.NewAgentRegistryClient(ctx, cfg.BrokerURL, "", "", nil)
	if err != nil {
		return nil, err
	}
	mcpToolsets, err := brokerclient.DiscoverMCPToolsets(ctx, regClient, cfg.BrokerURL, nil)
	if err != nil {
		return nil, err
	}
	toolsets = append(toolsets, mcpToolsets...)

	a, err := llmagent.New(llmagent.Config{
		Name:  "sre_triage_agent",
		Model: llm,
		InstructionProvider: func(agent.ReadonlyContext) (string, error) {
			return agentruntime.DefaultInstruction, nil
		},
		Tools:    tools,
		Toolsets: toolsets,
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

func TestModule04ZeroComputeRemediationGateApproveAndReject(t *testing.T) {
	ledger := ledgersvc.NewServer(ledgersvc.Options{
		Config:    ledgersvc.RuntimeConfig{MaxOpenConns: 2, IdleTimeoutSeconds: 0, AcquireTimeoutMs: 60},
		LogOutput: io.Discard,
	})
	t.Cleanup(ledger.Close)
	_ = ledger.ExecuteTransfer(context.Background(), "bad-1")
	_ = ledger.ExecuteTransfer(context.Background(), "bad-2")
	ledgerTS := httptest.NewServer(ledger.Handler())
	t.Cleanup(ledgerTS.Close)

	// Confirm Target Service starts degraded (503 Service Unavailable).
	hzBefore, err := http.Get(ledgerTS.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	hzBefore.Body.Close()
	if hzBefore.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("initial ledger-api /healthz = %d, want 503", hzBefore.StatusCode)
	}

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

	actorWorkspaceDir := t.TempDir()
	var actorTS *httptest.Server
	getActor := func() *httptest.Server {
		if actorTS != nil {
			return actorTS
		}
		srv, err := agentruntime.NewServer(agentruntime.Config{
			WorkspaceDir: actorWorkspaceDir,
			BrokerURL:    brokerURL,
			ModelName:    "gemini-3.8-flash",
			TemplateName: "sre-agent-v4",
			BuildRunner:  buildModule04Runner,
		})
		if err != nil {
			t.Fatalf("agentruntime.NewServer(sre-agent-v4): %v", err)
		}
		actorTS = httptest.NewServer(srv.Handler())
		t.Cleanup(actorTS.Close)
		return actorTS
	}

	routerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fakeATE.MarkRunning("workshop-301", "inc-402", "sre-workers-0")
		target := getActor()
		fwdReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.URL+r.URL.RequestURI(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fwdReq.Header = r.Header.Clone()
		resp, err := target.Client().Do(fwdReq)
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
		DefaultTemplate: "sre-agent-v4",
		SelfURL:         brokerURL,
		RouterURL:       routerTS.URL,
		OpsHubURL:       opsTS.URL,
		LedgerAPIURL:    ledgerTS.URL,
		IdleAfter:       10 * time.Minute, // Large IdleAfter proves immediate Remediation Gate suspend!
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

	dispatchRemediate := func() broker.ChatResponse {
		t.Helper()
		b, _ := json.Marshal(broker.ChatRequest{
			IncidentID: "INC-402",
			Template:   "sre-agent-v4",
			Prompt:     "Stage and remediate INC-402 end-to-end: query MCP logs and health, patch config/pool.yaml and pool/store.go on branch fix/inc-402-pool-leak, verify with ledger-verify, push the branch, and apply_remediation_pr.",
		})
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

	// 1. Trigger remediation -> verify MCP tools, workspace verification, push_git_branch, and immediate 0 vCPU suspend!
	gated1 := dispatchRemediate()
	if !gated1.RemediationGatePending || gated1.ConfirmationCallID == "" || gated1.CallID == "" {
		t.Fatalf("gated1 = %+v; want RemediationGatePending=true with non-empty ConfirmationCallID & CallID", gated1)
	}
	if gated1.ToolName != "apply_remediation_pr" {
		t.Errorf("gated1.ToolName = %q, want apply_remediation_pr", gated1.ToolName)
	}
	if gated1.ToolArgs["branch"] != "fix/inc-402-pool-leak" {
		t.Errorf("gated1.ToolArgs = %+v, want branch=fix/inc-402-pool-leak", gated1.ToolArgs)
	}
	if gated1.ConfirmationHint == "" {
		t.Errorf("gated1.ConfirmationHint is empty")
	}
	if !gated1.Workspace.VerifyOK || gated1.Workspace.Branch != "fix/inc-402-pool-leak" {
		t.Errorf("gated1.Workspace = %+v, want VerifyOK=true on fix/inc-402-pool-leak", gated1.Workspace)
	}
	seenTools := map[string]bool{}
	for _, st := range gated1.ToolSteps {
		seenTools[st.Name] = true
	}
	for _, want := range []string{"query_service_error_logs", "get_deployment_health", "stage_git_patch", "run_workspace_command", "push_git_branch"} {
		if !seenTools[want] {
			t.Errorf("Turn 1 missing expected tool step %q (got %v)", want, seenTools)
		}
	}

	// Actor must be IMMEDIATELY suspended in Cloud Storage (0 vCPU) without waiting on IdleAfter!
	if a := fakeATE.Actor("workshop-301", "inc-402"); a == nil || a.GetStatus().GetState() != ateapipb.ActorState_ACTOR_STATE_SUSPENDED {
		t.Fatalf("inc-402 state after Remediation Gate = %v, want ACTOR_STATE_SUSPENDED (0 vCPU)", a)
	}

	// Verify /api/incidents exposes pending_gate while actor stays SUSPENDED and ledger-api stays 503.
	incResp, err := http.Get(brokerURL + "/api/incidents")
	if err != nil {
		t.Fatal(err)
	}
	var incDoc struct {
		Incidents []struct {
			ID          string                     `json:"id"`
			Status      string                     `json:"status"`
			ActorState  string                     `json:"actor_state"`
			PendingGate *broker.RemediationGateDTO `json:"pending_gate"`
		} `json:"incidents"`
	}
	_ = json.NewDecoder(incResp.Body).Decode(&incDoc)
	incResp.Body.Close()
	if len(incDoc.Incidents) == 0 || incDoc.Incidents[0].PendingGate == nil || incDoc.Incidents[0].PendingGate.CallID != gated1.CallID {
		t.Fatalf("/api/incidents pending_gate = %+v, want call_id=%s", incDoc.Incidents, gated1.CallID)
	}

	// Verify zero-wake Session Ledger persisted adk_request_confirmation.
	ledResp, err := http.Get(brokerURL + "/api/ledger/sessions/inc-402")
	if err != nil {
		t.Fatal(err)
	}
	ledBytes, _ := io.ReadAll(ledResp.Body)
	ledResp.Body.Close()
	if !strings.Contains(string(ledBytes), "adk_request_confirmation") {
		t.Errorf("Session Ledger detail missing adk_request_confirmation event: %s", string(ledBytes))
	}

	// 2. Test Reject first: clears gate without merging PR or reloading ledger-api!
	rejResp, err := http.Post(brokerURL+"/api/incidents/INC-402/remediation/reject", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /remediation/reject: %v", err)
	}
	var rejChat broker.ChatResponse
	_ = json.NewDecoder(rejResp.Body).Decode(&rejChat)
	rejResp.Body.Close()
	if rejResp.StatusCode != http.StatusOK || rejChat.RemediationGatePending {
		t.Fatalf("reject status=%d out=%+v; want 200 OK with gate cleared", rejResp.StatusCode, rejChat)
	}
	hzAfterReject, _ := http.Get(ledgerTS.URL + "/healthz")
	hzAfterReject.Body.Close()
	if hzAfterReject.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ledger-api /healthz after reject = %d, want 503 (unchanged)", hzAfterReject.StatusCode)
	}

	// 3. Re-trigger remediation gate, test CSRF protection, then Approve!
	gated2 := dispatchRemediate()
	if !gated2.RemediationGatePending || gated2.CallID == "" {
		t.Fatalf("gated2 = %+v; want RemediationGatePending=true", gated2)
	}
	if a := fakeATE.Actor("workshop-301", "inc-402"); a == nil || a.GetStatus().GetState() != ateapipb.ActorState_ACTOR_STATE_SUSPENDED {
		t.Fatalf("inc-402 state before approve = %v, want ACTOR_STATE_SUSPENDED", a)
	}

	sessResp, err := http.Get(brokerURL + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	var sessDoc struct {
		CSRFToken string `json:"csrf_token"`
	}
	_ = json.NewDecoder(sessResp.Body).Decode(&sessDoc)
	cookies := sessResp.Cookies()
	sessResp.Body.Close()

	noCSRFApprove, _ := http.NewRequest(http.MethodPost, brokerURL+"/api/incidents/INC-402/remediation/approve", strings.NewReader(`{}`))
	noCSRFApprove.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		noCSRFApprove.AddCookie(c)
	}
	noCSRFResp, err := http.DefaultClient.Do(noCSRFApprove)
	if err != nil {
		t.Fatal(err)
	}
	noCSRFResp.Body.Close()
	if noCSRFResp.StatusCode != http.StatusForbidden {
		t.Errorf("cookie-bearing approve without CSRF status = %d, want 403", noCSRFResp.StatusCode)
	}

	startResume := time.Now()
	approveReq, _ := http.NewRequest(http.MethodPost, brokerURL+"/api/incidents/INC-402/remediation/approve", strings.NewReader(`{}`))
	approveReq.Header.Set("Content-Type", "application/json")
	approveReq.Header.Set("X-CSRF-Token", sessDoc.CSRFToken)
	for _, c := range cookies {
		approveReq.AddCookie(c)
	}
	appResp, err := http.DefaultClient.Do(approveReq)
	if err != nil {
		t.Fatalf("POST /remediation/approve: %v", err)
	}
	var appChat broker.ChatResponse
	_ = json.NewDecoder(appResp.Body).Decode(&appChat)
	appResp.Body.Close()
	resumeElapsed := time.Since(startResume)

	if appResp.StatusCode != http.StatusOK {
		t.Fatalf("approve status = %d, want 200 OK", appResp.StatusCode)
	}
	if resumeElapsed >= 400*time.Millisecond {
		t.Errorf("resume + apply_remediation_pr took %s, want < 400ms", resumeElapsed)
	}
	if appChat.ResumeDurationMs < 1 || appChat.ResumeDurationMs >= 400 {
		t.Errorf("ResumeDurationMs = %d, want [1, 400)", appChat.ResumeDurationMs)
	}
	if appChat.RemediationGatePending {
		t.Errorf("expected RemediationGatePending=false after approval")
	}
	if !strings.Contains(appChat.Reply, "PR-1") || !strings.Contains(appChat.Reply, "200 OK") {
		t.Errorf("approved Reply = %q, want PR-1 and 200 OK", appChat.Reply)
	}

	// Verify ledger-api /healthz is now 200 OK and INC-402 is RESOLVED on ops-hub!
	hzAfterApprove, err := http.Get(ledgerTS.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	hzAfterApprove.Body.Close()
	if hzAfterApprove.StatusCode != http.StatusOK {
		t.Fatalf("ledger-api /healthz after approve = %d, want 200 OK", hzAfterApprove.StatusCode)
	}

	incAfterResp, err := http.Get(brokerURL + "/api/incidents")
	if err != nil {
		t.Fatal(err)
	}
	var incAfterDoc struct {
		Incidents []struct {
			ID             string                     `json:"id"`
			Status         string                     `json:"status"`
			ResolutionPRID string                     `json:"resolution_pr_id"`
			PendingGate    *broker.RemediationGateDTO `json:"pending_gate"`
		} `json:"incidents"`
	}
	_ = json.NewDecoder(incAfterResp.Body).Decode(&incAfterDoc)
	incAfterResp.Body.Close()
	if len(incAfterDoc.Incidents) == 0 || incAfterDoc.Incidents[0].Status != "RESOLVED" || incAfterDoc.Incidents[0].ResolutionPRID != "PR-1" || incAfterDoc.Incidents[0].PendingGate != nil {
		t.Fatalf("post-approve /api/incidents = %+v, want INC-402 RESOLVED via PR-1 with nil PendingGate", incAfterDoc.Incidents)
	}
}

func TestModule04ConsoleGateBannerAndCatalogRegistryTables(t *testing.T) {
	ledger := ledgersvc.NewServer(ledgersvc.Options{
		Config:    ledgersvc.RuntimeConfig{MaxOpenConns: 2, IdleTimeoutSeconds: 0, AcquireTimeoutMs: 60},
		LogOutput: io.Discard,
	})
	t.Cleanup(ledger.Close)
	ledgerTS := httptest.NewServer(ledger.Handler())
	t.Cleanup(ledgerTS.Close)

	ops, err := opshub.NewServer(opshub.Options{
		DataDir:      t.TempDir(),
		SimNamespace: "prod-sim",
		LedgerAPIURL: ledgerTS.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	opsTS := httptest.NewServer(ops.Handler())
	t.Cleanup(opsTS.Close)

	gwSrv, err := broker.NewServer(broker.Config{
		Namespace:       "workshop-301",
		Atespace:        "workshop-301",
		SimNamespace:    "prod-sim",
		ProjectID:       "demo-project",
		Region:          "us-central1",
		MockLLM:         true,
		DefaultTemplate: "sre-agent-v4",
		OpsHubURL:       opsTS.URL,
		LedgerAPIURL:    ledgerTS.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gwSrv.Close() })
	gwTS := httptest.NewServer(gwSrv.Handler())
	t.Cleanup(gwTS.Close)

	catHTMLResp, err := http.Get(gwTS.URL + "/catalog")
	if err != nil {
		t.Fatal(err)
	}
	catHTMLBytes, _ := io.ReadAll(catHTMLResp.Body)
	catHTMLResp.Body.Close()
	catHTML := string(catHTMLBytes)

	for _, want := range []string{
		`id="gate-banner"`,
		`id="gate-actor-pill"`,
		`Suspended · 0 vCPU`,
		`Approve remediation`,
		`Reject`,
		`id="a2a-agents-body"`,
		`id="mcp-servers-body"`,
	} {
		if !strings.Contains(catHTML, want) {
			t.Errorf("/catalog HTML missing expected snippet %q", want)
		}
	}

	jsResp, err := http.Get(gwTS.URL + "/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	jsBytes, _ := io.ReadAll(jsResp.Body)
	jsResp.Body.Close()
	jsBody := string(jsBytes)

	for _, wantJS := range []string{
		"/remediation/approve",
		"/remediation/reject",
		"a2a-agents-body",
		"mcp-servers-body",
		"readOnlyHint",
		"destructiveHint",
	} {
		if !strings.Contains(jsBody, wantJS) {
			t.Errorf("/assets/app.js missing %q", wantJS)
		}
	}
	for _, banned := range []string{
		"inner" + "HTML",
		"outer" + "HTML",
		"insertAdjacent" + "HTML",
		"document." + "write",
		"local" + "Storage",
		"session" + "Storage",
	} {
		if strings.Contains(jsBody, banned) {
			t.Errorf("/assets/app.js contains prohibited DOM/storage API %q", banned)
		}
	}
}

func TestRemediationResumeDurationExcludesActorTurnExecution(t *testing.T) {
	fakeRouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/turn" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		time.Sleep(30 * time.Millisecond)
		// Simulate a 25s inside-actor ADK turn so total HTTP duration would exceed 400ms
		// if ResumeDurationMs failed to subtract out.DurationMs.
		_ = json.NewEncoder(w).Encode(agentruntime.TurnResponse{
			IncidentID: "INC-402",
			SessionID:  "inc-402",
			Template:   "sre-agent-v4",
			Reply:      "Merged PR-1; /healthz 200 OK",
			DurationMs: 10,
		})
	}))
	t.Cleanup(fakeRouter.Close)

	gwSrv, err := broker.NewServer(broker.Config{
		Namespace:       "workshop-301",
		Atespace:        "workshop-301",
		SimNamespace:    "prod-sim",
		ProjectID:       "demo-project",
		Region:          "us-central1",
		MockLLM:         true,
		DefaultTemplate: "sre-agent-v4",
		RouterURL:       fakeRouter.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gwSrv.Close() })
	gwTS := httptest.NewServer(gwSrv.Handler())
	t.Cleanup(gwTS.Close)

	resp, err := http.Post(
		gwTS.URL+"/api/incidents/INC-402/remediation/approve",
		"application/json",
		strings.NewReader(`{"call_id":"call-confirm-1"}`),
	)
	if err != nil {
		t.Fatalf("POST /remediation/approve: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	rawApprove, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	var out broker.ChatResponse
	if err := json.Unmarshal(rawApprove, &out); err != nil {
		t.Fatalf("decode ChatResponse: %v", err)
	}
	if out.ResumeDurationMs < 15 || out.ResumeDurationMs >= 400 {
		t.Fatalf("ResumeDurationMs = %d, want wake overhead in [15, 400)", out.ResumeDurationMs)
	}
	var rawMap map[string]any
	if err := json.Unmarshal(rawApprove, &rawMap); err != nil {
		t.Fatalf("decode raw JSON map: %v", err)
	}
	gatePendingVal, present := rawMap["remediation_gate_pending"]
	if !present || gatePendingVal != false {
		t.Fatalf("raw approval JSON remediation_gate_pending = %v (present=%t), want explicit false", gatePendingVal, present)
	}

	rawTurn, err := json.Marshal(agentruntime.TurnResponse{
		IncidentID: "INC-402",
		SessionID:  "inc-402",
		Template:   "sre-agent-v4",
		Reply:      "ok",
	})
	if err != nil {
		t.Fatalf("marshal TurnResponse: %v", err)
	}
	var rawTurnMap map[string]any
	if err := json.Unmarshal(rawTurn, &rawTurnMap); err != nil {
		t.Fatalf("unmarshal TurnResponse map: %v", err)
	}
	if v, ok := rawTurnMap["remediation_gate_pending"]; !ok || v != false {
		t.Fatalf("raw TurnResponse JSON remediation_gate_pending = %v (present=%t), want explicit false", v, ok)
	}
}
