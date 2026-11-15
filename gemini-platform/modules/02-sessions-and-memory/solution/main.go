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

// Command agent is the Platform SRE Triage Agent running inside an Agent Substrate
// gVisor Incident Actor on port 80.
//
// Module 02 completed solution: managed Gemini Sessions (Session Ledger) and
// Gemini Memory Bank (Consolidated Memory) routed keylessly via sre-gateway.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/agentruntime"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/brokerclient"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/ledgerverify"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/loadmemorytool"
	"google.golang.org/adk/v2/tool/preloadmemorytool"
)

func main() {
	if maybeRunLedgerVerify(os.Args) {
		return
	}

	cfg := agentruntime.Config{
		WorkspaceDir: envOr("WORKSPACE_DIR", "/workspace"),
		BrokerURL:    os.Getenv("BROKER_URL"),
		ModelName:    envOr("GEMINI_MODEL", "gemini-3.8-flash"),
		TemplateName: envOr("ACTOR_TEMPLATE", "sre-agent-v2"),
		BuildRunner:  buildRunner,
	}

	srv, err := agentruntime.NewServer(cfg)
	if err != nil {
		log.Fatalf("agent: %v", err)
	}
	addr := listenAddr()
	log.Printf("sre-agent (%s) listening on %s (workspace=%s broker=%s)", cfg.TemplateName, addr, cfg.WorkspaceDir, cfg.BrokerURL)
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}

func buildRunner(ctx context.Context, cfg agentruntime.Config, workspaceTools []tool.Tool) (*runner.Runner, error) {
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
	memoryPlugin, err := brokerclient.NewMemoryConsolidationPlugin(sessionSvc, memorySvc)
	if err != nil {
		return nil, err
	}

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

func listenAddr() string {
	if addr := strings.TrimSpace(os.Getenv("LISTEN_ADDR")); addr != "" {
		return addr
	}
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "80"
	}
	if strings.Contains(port, ":") {
		return port
	}
	return ":" + port
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func maybeRunLedgerVerify(args []string) bool {
	if len(args) == 0 {
		return false
	}
	var rest []string
	switch {
	case filepath.Base(args[0]) == "ledger-verify":
		rest = args[1:]
	case len(args) >= 2 && (args[1] == "ledger-verify" || args[1] == "verify"):
		rest = args[2:]
	default:
		return false
	}
	dir := "."
	if len(rest) > 0 && strings.TrimSpace(rest[0]) != "" {
		dir = rest[0]
	}
	rep, err := ledgerverify.VerifyDir(dir)
	if rep == nil {
		fmt.Fprintf(os.Stderr, "ledger-verify: %v\n", err)
		os.Exit(2)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rep)
	if err != nil {
		os.Exit(1)
	}
	return true
}
