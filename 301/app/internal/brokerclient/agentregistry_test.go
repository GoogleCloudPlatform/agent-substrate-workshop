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

package brokerclient_test

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gke-demos/substrate-agent-platform/app/internal/broker"
	"github.com/gke-demos/substrate-agent-platform/app/internal/brokerclient"
	"github.com/gke-demos/substrate-agent-platform/app/internal/ledgersvc"
	"github.com/gke-demos/substrate-agent-platform/app/internal/opshub"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type testReadonlyContext struct {
	context.Context
}

func (testReadonlyContext) UserContent() *genai.Content          { return nil }
func (testReadonlyContext) InvocationID() string                 { return "inv-test" }
func (testReadonlyContext) AgentName() string                    { return "sre_triage_agent" }
func (testReadonlyContext) ReadonlyState() session.ReadonlyState { return nil }
func (testReadonlyContext) UserID() string                       { return "sre-operator" }
func (testReadonlyContext) AppName() string                      { return "substrate-sre-triage" }
func (testReadonlyContext) SessionID() string                    { return "inc-402" }
func (testReadonlyContext) Branch() string                       { return "" }

var _ agent.ReadonlyContext = testReadonlyContext{}

func TestAgentRegistryDiscoveryAndMCPToolsets(t *testing.T) {
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

	gwSrv, err := broker.NewServer(broker.Config{
		Namespace:    "workshop-301",
		Atespace:     "workshop-301",
		SimNamespace: "prod-sim",
		ProjectID:    "demo-project",
		Region:       "us-central1",
		MockLLM:      true,
		OpsHubURL:    opsTS.URL,
		LedgerAPIURL: ledgerTS.URL,
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = gwSrv.Close() })

	gwTS := httptest.NewServer(gwSrv.Handler())
	t.Cleanup(gwTS.Close)

	ctx := context.Background()
	regClient, err := brokerclient.NewAgentRegistryClient(ctx, gwTS.URL, "demo-project", "us-central1", gwTS.Client())
	if err != nil {
		t.Fatalf("NewAgentRegistryClient: %v", err)
	}

	agentsResp, err := regClient.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agentsResp.Agents) == 0 || agentsResp.Agents[0].DisplayName != "Platform SRE Triage Agent" {
		t.Fatalf("ListAgents = %+v, want Platform SRE Triage Agent", agentsResp.Agents)
	}

	mcpResp, err := regClient.ListMCPServers(ctx)
	if err != nil {
		t.Fatalf("ListMCPServers: %v", err)
	}
	byID := map[string]bool{}
	var sawReadOnlyLog, sawDestructiveTrap bool
	for _, srv := range mcpResp.MCPServers {
		byID[srv.MCPServerID] = true
		for _, tl := range srv.Tools {
			if tl.Name == "query_service_error_logs" && tl.Annotations != nil && tl.Annotations.ReadOnlyHint && !tl.Annotations.DestructiveHint {
				sawReadOnlyLog = true
			}
			if tl.Name == "delete_production_namespace" && tl.Annotations != nil && tl.Annotations.DestructiveHint && !tl.Annotations.ReadOnlyHint {
				sawDestructiveTrap = true
			}
		}
	}
	for _, wantID := range []string{"logging-mcp", "container-mcp", "ops-hub-mcp"} {
		if !byID[wantID] {
			t.Errorf("ListMCPServers missing %q (got %v)", wantID, byID)
		}
	}
	if !sawReadOnlyLog {
		t.Errorf("expected query_service_error_logs with readOnlyHint=true")
	}
	if !sawDestructiveTrap {
		t.Errorf("expected delete_production_namespace with destructiveHint=true")
	}

	toolsets, err := brokerclient.DiscoverMCPToolsets(ctx, regClient, gwTS.URL, nil)
	if err != nil {
		t.Fatalf("DiscoverMCPToolsets: %v", err)
	}
	t.Cleanup(func() {
		for _, ts := range toolsets {
			if c, ok := ts.(io.Closer); ok {
				_ = c.Close()
			}
		}
	})
	if len(toolsets) < 3 {
		t.Fatalf("DiscoverMCPToolsets len = %d, want >= 3", len(toolsets))
	}

	discoveredTools := map[string]bool{}
	rctx := testReadonlyContext{Context: ctx}
	for _, ts := range toolsets {
		tls, err := ts.Tools(rctx)
		if err != nil {
			t.Fatalf("toolset %s Tools(): %v", ts.Name(), err)
		}
		for _, tl := range tls {
			discoveredTools[tl.Name()] = true
		}
	}
	for _, wantTool := range []string{"query_service_error_logs", "get_deployment_health", "get_incident", "get_service_slo", "delete_production_namespace"} {
		if !discoveredTools[wantTool] {
			t.Errorf("MCPToolset discovery missing live MCP tool %q (got %v)", wantTool, discoveredTools)
		}
	}
	if discoveredTools["apply_remediation_pr"] {
		t.Errorf("DiscoverMCPToolsets should filter apply_remediation_pr so confirmation-gated functiontool owns it without collision")
	}
}
