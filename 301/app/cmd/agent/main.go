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
// Modules 01-04 ask you to complete the sections marked WORKSHOP(module-N).
// Completed per-module references live in modules/NN-*/solution/main.go.
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

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/agentruntime"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ledgerverify"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"
)

func main() {
	if maybeRunLedgerVerify(os.Args) {
		return
	}

	cfg := agentruntime.Config{
		WorkspaceDir: envOr("WORKSPACE_DIR", "/workspace"),
		BrokerURL:    os.Getenv("BROKER_URL"),
		ModelName:    envOr("GEMINI_MODEL", "gemini-3.8-flash"),
		TemplateName: envOr("ACTOR_TEMPLATE", "sre-agent-v1"),
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
	// WORKSHOP(module-1): Wire brokerclient.NewModel, llmagent.New, and runner.New with workspaceTools.
	_ = ctx
	_ = cfg
	_ = workspaceTools

	// WORKSHOP(module-2): Replace session.InMemoryService() with brokerclient.NewSessionService and wire brokerclient.NewMemoryService.

	// WORKSHOP(module-3): Attach SkillToolset backed by RegistrySkillSource so procedural runbooks load without rebuilding the Actor.

	// WORKSHOP(module-4): Discover MCP toolsets from Agent Registry and enable RequireConfirmation on apply_remediation_pr.

	return nil, nil
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
