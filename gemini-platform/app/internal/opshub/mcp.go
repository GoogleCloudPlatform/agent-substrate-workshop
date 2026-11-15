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

package opshub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

type mcpToolDef struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	InputSchema map[string]any     `json:"inputSchema"`
	Annotations mcpToolAnnotations `json:"annotations"`
}

func opsHubMCPTools() []mcpToolDef {
	return []mcpToolDef{
		{
			Name:        "get_incident",
			Description: "Fetch an incident ticket and diagnostic metadata by incident ID (e.g. INC-402 or INC-409).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"incident_id": map[string]any{"type": "string", "description": "Incident identifier such as INC-402."},
				},
				"required": []string{"incident_id"},
			},
			Annotations: mcpToolAnnotations{
				Title:           "Get incident ticket",
				ReadOnlyHint:    true,
				DestructiveHint: false,
				IdempotentHint:  true,
				OpenWorldHint:   false,
			},
		},
		{
			Name:        "list_commits",
			Description: "List recent git commits on a branch in ledger-api.git.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"branch": map[string]any{"type": "string", "description": "Branch name (defaults to main)."},
					"limit":  map[string]any{"type": "integer", "description": "Maximum commits to return (1..50)."},
				},
			},
			Annotations: mcpToolAnnotations{
				Title:           "List repository commits",
				ReadOnlyHint:    true,
				DestructiveHint: false,
				IdempotentHint:  true,
				OpenWorldHint:   false,
			},
		},
		{
			Name:        "get_service_slo",
			Description: "Inspect live health, connection pool saturation, and latency SLO compliance for ledger-api.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"service": map[string]any{"type": "string", "description": "Target service name (ledger-api)."},
				},
			},
			Annotations: mcpToolAnnotations{
				Title:           "Get service SLO status",
				ReadOnlyHint:    true,
				DestructiveHint: false,
				IdempotentHint:  true,
				OpenWorldHint:   false,
			},
		},
		{
			Name:        "apply_remediation_pr",
			Description: "Validate and merge a pushed git remediation branch into main and reload live ledger-api configuration.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"incident_id": map[string]any{"type": "string", "description": "Incident ID resolved by this PR (e.g. INC-402)."},
					"branch":      map[string]any{"type": "string", "description": "Pushed git branch name in ledger-api.git."},
					"title":       map[string]any{"type": "string", "description": "Pull request summary title."},
				},
				"required": []string{"incident_id", "branch", "title"},
			},
			Annotations: mcpToolAnnotations{
				Title:           "Apply remediation pull request",
				ReadOnlyHint:    false,
				DestructiveHint: true,
				IdempotentHint:  false,
				OpenWorldHint:   false,
			},
		},
		{
			Name:        "drop_ledger_database",
			Description: "Permanently drop all ledger database tables in prod-sim (emergency purge).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"confirm": map[string]any{"type": "boolean", "description": "Confirmation flag."},
					"reason":  map[string]any{"type": "string", "description": "Audit reason."},
				},
				"required": []string{"confirm"},
			},
			Annotations: mcpToolAnnotations{
				Title:           "Drop ledger database",
				ReadOnlyHint:    false,
				DestructiveHint: true,
				IdempotentHint:  false,
				OpenWorldHint:   false,
			},
		},
	}
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Allow", "POST")
			http.Error(w, "standalone SSE stream not enabled on stateless MCP endpoint", http.StatusMethodNotAllowed)
			return
		}
		writeJSONResp(w, http.StatusOK, map[string]any{
			"name":     "ops-hub-mcp",
			"version":  "1.0.0",
			"protocol": "mcp-streamable-http",
		})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeJSONResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	var req jsonRPCRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodyLen))
	if err := dec.Decode(&req); err != nil {
		writeRPCError(w, http.StatusBadRequest, json.RawMessage("null"), -32700, "invalid JSON-RPC payload")
		return
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		// JSON-RPC notifications (such as notifications/initialized) receive HTTP 202 Accepted.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		protoVer := "2025-03-26"
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(req.Params) > 0 && json.Unmarshal(req.Params, &initParams) == nil && initParams.ProtocolVersion != "" {
			protoVer = initParams.ProtocolVersion
		}
		sessID, err := randTokenHex(16)
		if err == nil {
			w.Header().Set("Mcp-Session-Id", sessID)
		}
		writeRPCResult(w, req.ID, map[string]any{
			"protocolVersion": protoVer,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    "ops-hub-mcp",
				"version": "1.0.0",
			},
		})

	case "ping":
		writeRPCResult(w, req.ID, map[string]any{})

	case "tools/list":
		writeRPCResult(w, req.ID, map[string]any{
			"tools": opsHubMCPTools(),
		})

	case "tools/call":
		s.handleMCPToolCall(w, r, req)

	default:
		writeRPCError(w, http.StatusNotFound, req.ID, -32601, fmt.Sprintf("unsupported MCP method %q", req.Method))
	}
}

func (s *Server) handleMCPToolCall(w http.ResponseWriter, r *http.Request, req jsonRPCRequest) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &call); err != nil {
		writeRPCError(w, http.StatusBadRequest, req.ID, -32602, "invalid tools/call params")
		return
	}

	switch call.Name {
	case "get_incident":
		var args struct {
			IncidentID string `json:"incident_id"`
		}
		_ = json.Unmarshal(call.Arguments, &args)
		inc, ok := s.getIncident(args.IncidentID)
		if !ok {
			writeToolText(w, req.ID, fmt.Sprintf("incident %q not found", args.IncidentID), true)
			return
		}
		b, _ := json.Marshal(inc)
		writeToolText(w, req.ID, string(b), false)

	case "list_commits":
		var args struct {
			Branch string `json:"branch"`
			Limit  int    `json:"limit"`
		}
		_ = json.Unmarshal(call.Arguments, &args)
		commits, err := s.listRepoCommits(r.Context(), args.Branch, args.Limit)
		if err != nil {
			writeToolText(w, req.ID, err.Error(), true)
			return
		}
		b, _ := json.Marshal(commits)
		writeToolText(w, req.ID, string(b), false)

	case "get_service_slo":
		statusPayload := s.fetchLiveLedgerSLO(r.Context())
		b, _ := json.Marshal(statusPayload)
		writeToolText(w, req.ID, string(b), false)

	case "apply_remediation_pr":
		var args struct {
			IncidentID string `json:"incident_id"`
			Branch     string `json:"branch"`
			Title      string `json:"title"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			writeRPCError(w, http.StatusBadRequest, req.ID, -32602, "invalid apply_remediation_pr arguments")
			return
		}

		operatorSub := ""
		require3LO := s.is3LORequired(r)
		hasAuthHeader := strings.TrimSpace(r.Header.Get("Authorization")) != ""
		if require3LO || hasAuthHeader {
			tokRec, ok := s.authenticateBearer(r)
			if !ok {
				w.Header().Set("WWW-Authenticate", `Bearer realm="ops-hub", scope="remediation:write"`)
				writeRPCError(w, http.StatusUnauthorized, req.ID, -32001, "3LO operator Bearer token required for apply_remediation_pr")
				return
			}
			if !hasOAuthScope(tokRec.scope, "remediation:write") {
				writeRPCError(w, http.StatusForbidden, req.ID, -32003, "3LO token missing required scope remediation:write")
				return
			}
			operatorSub = tokRec.operatorSub
		}

		agentID := strings.TrimSpace(r.Header.Get("X-Agent-Identity-Principal"))
		if agentID == "" {
			agentID = strings.TrimSpace(r.Header.Get("X-Agent-Identity"))
		}
		delegated := strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Agentic-Delegation")), "true")
		pr, code, errMsg := s.applyRemediationPR(r.Context(), args.IncidentID, args.Branch, args.Title, operatorSub, agentID, delegated)
		if code != http.StatusCreated {
			writeToolText(w, req.ID, errMsg, true)
			return
		}
		b, _ := json.Marshal(pr)
		writeToolText(w, req.ID, string(b), false)

	case "drop_ledger_database":
		writeRPCError(w, http.StatusForbidden, req.ID, -32003, "destructive tool drop_ledger_database is blocked by Operations Hub safety policy")

	default:
		writeRPCError(w, http.StatusNotFound, req.ID, -32602, fmt.Sprintf("unknown tool %q", call.Name))
	}
}

func (s *Server) fetchLiveLedgerSLO(ctx context.Context) any {
	if s.ledgerAPIURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.ledgerAPIURL+"/api/status", nil)
		if err == nil {
			resp, err := s.httpClient.Do(req)
			if err == nil {
				raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
				resp.Body.Close()
				if readErr == nil && resp.StatusCode == http.StatusOK {
					var parsed map[string]any
					if json.Unmarshal(raw, &parsed) == nil {
						return parsed
					}
				}
			}
		}
	}
	s.mu.Lock()
	cfg := s.activePoolCfg
	s.mu.Unlock()
	return map[string]any{
		"service":        "ledger-api",
		"slo_met":        cfg.LeakFixed,
		"pool_exhausted": !cfg.LeakFixed,
		"max_open_conns": cfg.MaxOpenConns,
	}
}

func (s *Server) is3LORequired(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("X-OpsHub-Require-3LO"), "true") {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.require3LO
}

func writeToolText(w http.ResponseWriter, id json.RawMessage, text string, isErr bool) {
	writeRPCResult(w, id, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"isError": isErr,
	})
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSONResp(w, http.StatusOK, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func writeRPCError(w http.ResponseWriter, statusCode int, id json.RawMessage, rpcCode int, message string) {
	writeJSONResp(w, statusCode, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    rpcCode,
			"message": message,
		},
	})
}
