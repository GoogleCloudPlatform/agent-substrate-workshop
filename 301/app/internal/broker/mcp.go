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

package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gke-demos/substrate-agent-platform/app/internal/brokerclient"
	"github.com/gke-demos/substrate-agent-platform/app/internal/telemetry"
)

const maxMCPBodyBytes = 1 << 20 // 1 MiB cap per MCP JSON-RPC payload.

type mcpCallerSource string

const (
	mcpCallerActor         mcpCallerSource = "actor"
	mcpCallerExternalProbe mcpCallerSource = "external_boundary_probe"
)

type mcpJSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mcpWireToolDef struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	InputSchema map[string]any             `json:"inputSchema"`
	Annotations RegistryToolAnnotationsDTO `json:"annotations"`
}

// handleMCPBroker serves /mcp/{serverID} across the Actor Boundary, dynamically
// enforcing Agent Registry readOnlyHint and destructiveHint annotations on every
// tools/call request.
func (s *Server) handleMCPBroker(w http.ResponseWriter, r *http.Request) {
	serverID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/mcp/"), "/")
	if serverID == "" || strings.Contains(serverID, "/") {
		http.NotFound(w, r)
		return
	}

	baseURL := s.resolveBaseURL(r)
	srv, serverFound := s.agentRegistry.lookupMCPServer(r.Context(), serverID, baseURL)
	if !serverFound {
		http.NotFound(w, r)
		return
	}

	if r.Method == http.MethodGet {
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Allow", "POST")
			http.Error(w, "standalone SSE stream not enabled on stateless MCP endpoint", http.StatusMethodNotAllowed)
			return
		}
		writeBrokerJSON(w, http.StatusOK, map[string]any{
			"name":     srv.MCPServerID,
			"version":  "1.0.0",
			"protocol": "mcp-streamable-http",
		})
		return
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPBodyBytes))
	if err != nil {
		writeMCPRPCError(w, http.StatusRequestEntityTooLarge, json.RawMessage("null"), -32700, "MCP request exceeds size limit")
		return
	}

	var req mcpJSONRPCRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeMCPRPCError(w, http.StatusBadRequest, json.RawMessage("null"), -32700, "invalid JSON-RPC payload")
		return
	}

	reqCtx, incomingSC, hasIncomingTrace := telemetry.ExtractHTTP(r.Context(), r.Header)
	_, hasActiveTurn := s.anyActiveActorTrace()
	var isExternalProbe bool
	if hasIncomingTrace {
		isExternalProbe = !s.isActorTurnTrace(incomingSC.TraceID)
	} else {
		isStreamableMCPClient := strings.Contains(r.Header.Get("Accept"), "text/event-stream") ||
			strings.TrimSpace(r.Header.Get(brokerclient.RemediationGateTokenHeader)) != ""
		isExternalProbe = !hasActiveTurn || !isStreamableMCPClient
		if isExternalProbe {
			// Isolate unassociated external probes on their own root TraceID so
			// startBrokerSpan never parents them onto an unrelated concurrent Actor turn.
			reqCtx = telemetry.ContextWithSpanContext(reqCtx, telemetry.SpanContext{
				TraceID: telemetry.NewTraceID(),
				SpanID:  telemetry.NewSpanID(),
				Flags:   "01",
			})
		}
	}
	r = r.WithContext(reqCtx)

	var (
		call         mcpToolCallParams
		callAnn      = defaultRegistryToolAnnotations()
		approvedGate bool
	)
	if req.Method == "tools/call" {
		if err := json.Unmarshal(req.Params, &call); err != nil {
			writeMCPRPCError(w, http.StatusBadRequest, req.ID, -32602, "invalid tools/call params")
			return
		}
		call.Name = strings.TrimSpace(call.Name)
		if call.Name == "" {
			writeMCPRPCError(w, http.StatusBadRequest, req.ID, -32602, "tool name is required")
			return
		}
		toolDTO, toolFound := findMCPTool(srv, call.Name)
		if toolFound {
			callAnn = toolDTO.Annotations
		}
		if callAnn.DestructiveHint || !callAnn.ReadOnlyHint {
			requestedIncID := extractMCPIncidentID(call.Arguments)
			approvedGate = serverID == "ops-hub-mcp" &&
				call.Name == "apply_remediation_pr" &&
				s.consumeGateApprovalTicket(r.Header.Get(brokerclient.RemediationGateTokenHeader), requestedIncID)
			if !approvedGate {
				callerSource := mcpCallerActor
				policyDetail := fmt.Sprintf("Blocked unapproved MCP tool %s (destructiveHint=%t, readOnlyHint=%t, registered=%t) with HTTP 403", call.Name, callAnn.DestructiveHint, callAnn.ReadOnlyHint, toolFound)
				if isExternalProbe {
					callerSource = mcpCallerExternalProbe
					policyDetail = fmt.Sprintf("Blocked external boundary security probe on unapproved MCP tool %s (destructiveHint=%t, readOnlyHint=%t, registered=%t) with HTTP 403", call.Name, callAnn.DestructiveHint, callAnn.ReadOnlyHint, toolFound)
				}
				_, _, finishMCP := s.startBrokerSpan(r.Context(), "mcp.call_tool", "CLIENT", "Agent Registry MCP", map[string]any{
					"gen_ai.operation.name": "execute_tool",
					"gen_ai.tool.name":      call.Name,
					"mcp.server.id":         serverID,
					"mcp.caller":            string(callerSource),
					"mcp.read_only_hint":    callAnn.ReadOnlyHint,
					"mcp.destructive_hint":  callAnn.DestructiveHint,
				})
				s.mcpDeniedCount.Add(1)
				s.metrics.ObserveMCPCall(serverID, "deny")
				finishMCP("ERROR", "MCP tool blocked by Agent Registry annotation policy", map[string]any{"mcp.verdict": "DENIED_DESTRUCTIVE_MCP"})
				s.recordPolicyEvent(PolicyEvent{
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Boundary:  "Agent Registry MCP (/mcp/" + serverID + ")",
					Action:    "tools/call (" + call.Name + ")",
					Verdict:   "DENIED_DESTRUCTIVE_MCP",
					Detail:    policyDetail,
				})
				writeMCPRPCError(w, http.StatusForbidden, req.ID, -32003,
					fmt.Sprintf("MCP tool %s on %s (destructiveHint=%t, readOnlyHint=%t) is blocked by Governed Broker Agent Registry policy", call.Name, serverID, callAnn.DestructiveHint, callAnn.ReadOnlyHint))
				return
			}
		}
	}

	switch serverID {
	case "logging-mcp":
		s.serveLoggingMCP(w, r, srv, req, call, callAnn)
	case "container-mcp":
		s.serveContainerMCP(w, r, srv, req, call, callAnn)
	case "ops-hub-mcp":
		s.serveOpsHubMCPProxy(w, r, req, rawBody, call, callAnn, approvedGate)
	default:
		s.serveDynamicCloudMCPProxy(w, r, srv, req, rawBody, call, callAnn)
	}
}

func extractMCPIncidentID(rawArgs json.RawMessage) string {
	if len(rawArgs) == 0 {
		return ""
	}
	var args struct {
		IncidentID string `json:"incident_id"`
	}
	_ = json.Unmarshal(rawArgs, &args)
	return strings.ToUpper(strings.TrimSpace(args.IncidentID))
}

func wireToolDefsFromRegistry(srv RegistryMCPServerDTO, schemaOverrides map[string]map[string]any) []mcpWireToolDef {
	out := make([]mcpWireToolDef, 0, len(srv.Tools))
	for _, tl := range srv.Tools {
		schema := schemaOverrides[tl.Name]
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, mcpWireToolDef{
			Name:        tl.Name,
			Description: tl.Description,
			InputSchema: schema,
			Annotations: tl.Annotations,
		})
	}
	return out
}

func (s *Server) serveLoggingMCP(
	w http.ResponseWriter,
	r *http.Request,
	srv RegistryMCPServerDTO,
	req mcpJSONRPCRequest,
	call mcpToolCallParams,
	callAnn RegistryToolAnnotationsDTO,
) {
	if isJSONRPCNotification(req) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		writeMCPInitializeResult(w, req, "logging-mcp")
	case "ping":
		writeMCPRPCResult(w, req.ID, map[string]any{})
	case "tools/list":
		schemas := map[string]map[string]any{
			"query_service_error_logs": {
				"type": "object",
				"properties": map[string]any{
					"service":   map[string]any{"type": "string", "description": "Target service name (defaults to ledger-api)."},
					"namespace": map[string]any{"type": "string", "description": "Kubernetes namespace (defaults to prod-sim)."},
					"severity":  map[string]any{"type": "string", "description": "Minimum log severity such as ERROR or WARNING."},
					"limit":     map[string]any{"type": "integer", "description": "Maximum log entries to return (1..20)."},
				},
			},
		}
		writeMCPRPCResult(w, req.ID, map[string]any{
			"tools": wireToolDefsFromRegistry(srv, schemas),
		})
	case "tools/call":
		if call.Name != "query_service_error_logs" {
			writeMCPRPCError(w, http.StatusNotFound, req.ID, -32602, fmt.Sprintf("unknown tool %q", call.Name))
			return
		}
		var args struct {
			Service   string `json:"service"`
			Namespace string `json:"namespace"`
			Severity  string `json:"severity"`
			Limit     int    `json:"limit"`
		}
		_ = json.Unmarshal(call.Arguments, &args)
		svc := firstNonEmpty(args.Service, "ledger-api")
		ns := firstNonEmpty(args.Namespace, s.cfg.SimNamespace)
		limit := args.Limit
		if limit <= 0 || limit > 20 {
			limit = 5
		}

		mcpCtx, _, finishMCP := s.startBrokerSpan(r.Context(), "mcp.call_tool", "CLIENT", "Agent Registry MCP", map[string]any{
			"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name":      call.Name,
			"mcp.server.id":         "logging-mcp",
			"mcp.read_only_hint":    callAnn.ReadOnlyHint,
			"mcp.destructive_hint":  callAnn.DestructiveHint,
		})
		payload := s.queryServiceErrorTelemetry(mcpCtx, svc, ns, limit)
		s.mcpAllowedCount.Add(1)
		s.metrics.ObserveMCPCall("logging-mcp", "allow")
		finishMCP("OK", "", map[string]any{"mcp.verdict": "ALLOW"})
		s.recordPolicyEvent(PolicyEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Boundary:  "Agent Registry MCP (/mcp/logging-mcp)",
			Action:    "tools/call (query_service_error_logs)",
			Verdict:   "ALLOW",
			Detail:    fmt.Sprintf("readOnlyHint=%t, destructiveHint=%t verified; queried error logs for %s in %s", callAnn.ReadOnlyHint, callAnn.DestructiveHint, svc, ns),
		})
		raw, _ := json.Marshal(payload)
		writeMCPToolText(w, req.ID, string(raw), false)
	default:
		writeMCPRPCError(w, http.StatusNotFound, req.ID, -32601, fmt.Sprintf("unsupported MCP method %q", req.Method))
	}
}

func (s *Server) serveContainerMCP(
	w http.ResponseWriter,
	r *http.Request,
	srv RegistryMCPServerDTO,
	req mcpJSONRPCRequest,
	call mcpToolCallParams,
	callAnn RegistryToolAnnotationsDTO,
) {
	if isJSONRPCNotification(req) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		writeMCPInitializeResult(w, req, "container-mcp")
	case "ping":
		writeMCPRPCResult(w, req.ID, map[string]any{})
	case "tools/list":
		schemas := map[string]map[string]any{
			"get_deployment_health": {
				"type": "object",
				"properties": map[string]any{
					"namespace":  map[string]any{"type": "string", "description": "Kubernetes namespace (defaults to prod-sim)."},
					"deployment": map[string]any{"type": "string", "description": "Deployment name (defaults to ledger-api)."},
				},
			},
			"delete_production_namespace": {
				"type": "object",
				"properties": map[string]any{
					"namespace": map[string]any{"type": "string", "description": "Target namespace to delete."},
					"reason":    map[string]any{"type": "string", "description": "Deletion reason."},
				},
				"required": []string{"namespace"},
			},
		}
		writeMCPRPCResult(w, req.ID, map[string]any{
			"tools": wireToolDefsFromRegistry(srv, schemas),
		})
	case "tools/call":
		if call.Name != "get_deployment_health" {
			writeMCPRPCError(w, http.StatusNotFound, req.ID, -32602, fmt.Sprintf("unknown tool %q", call.Name))
			return
		}
		var args struct {
			Namespace  string `json:"namespace"`
			Deployment string `json:"deployment"`
		}
		_ = json.Unmarshal(call.Arguments, &args)
		ns := firstNonEmpty(args.Namespace, s.cfg.SimNamespace)
		dep := firstNonEmpty(args.Deployment, "ledger-api")

		mcpCtx, _, finishMCP := s.startBrokerSpan(r.Context(), "mcp.call_tool", "CLIENT", "Agent Registry MCP", map[string]any{
			"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name":      call.Name,
			"mcp.server.id":         "container-mcp",
			"mcp.read_only_hint":    callAnn.ReadOnlyHint,
			"mcp.destructive_hint":  callAnn.DestructiveHint,
		})
		health := s.inspectDeploymentHealth(mcpCtx, ns, dep)
		s.mcpAllowedCount.Add(1)
		s.metrics.ObserveMCPCall("container-mcp", "allow")
		finishMCP("OK", "", map[string]any{"mcp.verdict": "ALLOW"})
		s.recordPolicyEvent(PolicyEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Boundary:  "Agent Registry MCP (/mcp/container-mcp)",
			Action:    "tools/call (" + call.Name + ")",
			Verdict:   "ALLOW",
			Detail:    fmt.Sprintf("readOnlyHint=%t, destructiveHint=%t verified; inspected %s/%s health", callAnn.ReadOnlyHint, callAnn.DestructiveHint, ns, dep),
		})
		raw, _ := json.Marshal(health)
		writeMCPToolText(w, req.ID, string(raw), false)
	default:
		writeMCPRPCError(w, http.StatusNotFound, req.ID, -32601, fmt.Sprintf("unsupported MCP method %q", req.Method))
	}
}

func (s *Server) serveOpsHubMCPProxy(
	w http.ResponseWriter,
	r *http.Request,
	req mcpJSONRPCRequest,
	rawBody []byte,
	call mcpToolCallParams,
	callAnn RegistryToolAnnotationsDTO,
	approvedGate bool,
) {
	var calledTool string
	var requestedIncID string
	var outboundBearer string
	var outboundDelegated bool
	var finishMCPSpan func(status, msg string, extra map[string]any)
	mcpCtx := r.Context()
	if req.Method == "tools/call" {
		calledTool = call.Name
		mcpCtx, _, finishMCPSpan = s.startBrokerSpan(r.Context(), "mcp.call_tool", "CLIENT", "Agent Registry MCP", map[string]any{
			"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name":      calledTool,
			"mcp.server.id":         "ops-hub-mcp",
			"mcp.read_only_hint":    callAnn.ReadOnlyHint,
			"mcp.destructive_hint":  callAnn.DestructiveHint,
		})
		requestedIncID = extractMCPIncidentID(call.Arguments)
		if approvedGate {
			principal := s.identity.OutboundPrincipal()
			_, _, finish3LO := s.startBrokerSpan(mcpCtx, "auth_manager.inject_3lo", "INTERNAL", "Auth Manager 3LO", map[string]any{
				"spiffe.principal": principal,
			})
			tok, meta, hasGrant := s.authManager.ActiveToken(mcpCtx, default3LOOperator)
			if s.cfg.Require3LO && !hasGrant && strings.TrimSpace(r.Header.Get("Authorization")) == "" {
				finish3LO("ERROR", "3LO operator consent required", map[string]any{"auth.delegated": false})
				if finishMCPSpan != nil {
					finishMCPSpan("ERROR", "3LO operator consent required", map[string]any{"mcp.verdict": "CONSENT_REQUIRED"})
				}
				s.recordPolicyEvent(PolicyEvent{
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Boundary:  "Auth Manager (3LO)",
					Action:    "tools/call (" + calledTool + ")",
					Verdict:   "CONSENT_REQUIRED",
					Detail:    "Blocked " + calledTool + " without active short-lived 3LO operator grant",
				})
				writeMCPRPCError(w, http.StatusUnauthorized, req.ID, -32001,
					"3LO operator consent required before executing "+calledTool)
				return
			}
			if hasGrant {
				outboundBearer = tok
				outboundDelegated = true
				finish3LO("OK", "", map[string]any{
					"auth.provider":    meta.provider,
					"auth.method":      meta.authMethod,
					"auth.operator":    meta.operatorSub,
					"auth.delegated":   true,
					"token.in_sandbox": false,
				})
				s.recordPolicyEvent(PolicyEvent{
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Boundary:  "Auth Manager (3LO)",
					Action:    "Inject3LOToken (ops-hub-mcp)",
					Verdict:   "ALLOW",
					Detail:    fmt.Sprintf("Attached short-lived 3LO grant (operator=%s, method=%s) + SPIFFE principal (%s) outside sandbox", meta.operatorSub, meta.authMethod, principal),
				})
			} else {
				finish3LO("OK", "", map[string]any{"auth.delegated": false})
			}
			s.mcpAllowedCount.Add(1)
			s.metrics.ObserveMCPCall("ops-hub-mcp", "allow")
			s.recordPolicyEvent(PolicyEvent{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Boundary:  "Agent Registry MCP (/mcp/ops-hub-mcp)",
				Action:    "tools/call (" + calledTool + ")",
				Verdict:   "ALLOW",
				Detail:    "Verified one-time Remediation Gate ticket and forwarded " + calledTool + " across Actor Boundary to ops-hub/mcp",
			})
		} else if calledTool != "" {
			s.mcpAllowedCount.Add(1)
			s.metrics.ObserveMCPCall("ops-hub-mcp", "allow")
			s.recordPolicyEvent(PolicyEvent{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Boundary:  "Agent Registry MCP (/mcp/ops-hub-mcp)",
				Action:    "tools/call (" + calledTool + ")",
				Verdict:   "ALLOW",
				Detail:    fmt.Sprintf("readOnlyHint=%t, destructiveHint=%t verified; proxied to ops-hub/mcp", callAnn.ReadOnlyHint, callAnn.DestructiveHint),
			})
		}
	}

	upReq, err := http.NewRequestWithContext(mcpCtx, http.MethodPost, s.cfg.OpsHubURL+"/mcp", bytes.NewReader(rawBody))
	if err != nil {
		writeMCPRPCError(w, http.StatusInternalServerError, req.ID, -32603, "construct upstream MCP request failed")
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	if accept := strings.TrimSpace(r.Header.Get("Accept")); accept != "" {
		upReq.Header.Set("Accept", accept)
	}
	if sessID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id")); sessID != "" {
		upReq.Header.Set("Mcp-Session-Id", sessID)
	}
	if outboundBearer != "" {
		upReq.Header.Set("Authorization", "Bearer "+outboundBearer)
	} else if authHdr := strings.TrimSpace(r.Header.Get("Authorization")); authHdr != "" {
		upReq.Header.Set("Authorization", authHdr)
	}
	if outboundDelegated {
		upReq.Header.Set("X-Agentic-Delegation", "true")
	}
	if s.cfg.Require3LO {
		upReq.Header.Set("X-OpsHub-Require-3LO", "true")
	}
	principal := s.identity.OutboundPrincipal()
	upReq.Header.Set("X-Agent-Identity", principal)
	upReq.Header.Set("X-Agent-Identity-Principal", principal)

	resp, err := s.opsClient.Do(upReq)
	if err != nil {
		if finishMCPSpan != nil {
			finishMCPSpan("ERROR", err.Error(), nil)
		}
		writeMCPRPCError(w, http.StatusBadGateway, req.ID, -32603, "ops-hub MCP unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	respBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if readErr != nil {
		if finishMCPSpan != nil {
			finishMCPSpan("ERROR", readErr.Error(), nil)
		}
		writeMCPRPCError(w, http.StatusBadGateway, req.ID, -32603, "read ops-hub MCP response failed")
		return
	}

	if calledTool == "get_incident" && resp.StatusCode == http.StatusOK && len(respBytes) > 0 {
		stage := "MCP get_incident " + firstNonEmpty(requestedIncID, "payload")
		if _, blockErr := s.modelArmor.ScreenUserPrompt(mcpCtx, stage, requestedIncID, string(respBytes)); blockErr != nil {
			if finishMCPSpan != nil {
				finishMCPSpan("ERROR", blockErr.Error(), map[string]any{"mcp.verdict": "MODEL_ARMOR_BLOCKED"})
			}
			writeMCPRPCError(w, http.StatusForbidden, req.ID, -32004, blockErr.Error())
			return
		}
	}

	if finishMCPSpan != nil {
		st := "OK"
		if resp.StatusCode >= 400 {
			st = "ERROR"
		}
		finishMCPSpan(st, "", map[string]any{"mcp.verdict": "ALLOW"})
	}

	if sessID := resp.Header.Get("Mcp-Session-Id"); sessID != "" {
		w.Header().Set("Mcp-Session-Id", sessID)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBytes)
}

func (s *Server) serveDynamicCloudMCPProxy(
	w http.ResponseWriter,
	r *http.Request,
	srv RegistryMCPServerDTO,
	req mcpJSONRPCRequest,
	rawBody []byte,
	call mcpToolCallParams,
	callAnn RegistryToolAnnotationsDTO,
) {
	if isJSONRPCNotification(req) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	targetURL := strings.TrimSpace(srv.upstreamURL)
	if targetURL == "" || strings.HasPrefix(targetURL, s.resolveBaseURL(r)+"/mcp/") {
		switch req.Method {
		case "initialize":
			writeMCPInitializeResult(w, req, srv.MCPServerID)
		case "ping":
			writeMCPRPCResult(w, req.ID, map[string]any{})
		case "tools/list":
			writeMCPRPCResult(w, req.ID, map[string]any{
				"tools": wireToolDefsFromRegistry(srv, nil),
			})
		default:
			writeMCPRPCError(w, http.StatusBadGateway, req.ID, -32603, fmt.Sprintf("MCP server %q has no reachable upstream endpoint", srv.MCPServerID))
		}
		return
	}

	mcpCtx := r.Context()
	var finishMCPSpan func(status, msg string, extra map[string]any)
	if req.Method == "tools/call" {
		mcpCtx, _, finishMCPSpan = s.startBrokerSpan(r.Context(), "mcp.call_tool", "CLIENT", "Agent Registry MCP", map[string]any{
			"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name":      call.Name,
			"mcp.server.id":         srv.MCPServerID,
			"mcp.read_only_hint":    callAnn.ReadOnlyHint,
			"mcp.destructive_hint":  callAnn.DestructiveHint,
		})
	}

	upReq, err := http.NewRequestWithContext(mcpCtx, http.MethodPost, targetURL, bytes.NewReader(rawBody))
	if err != nil {
		if finishMCPSpan != nil {
			finishMCPSpan("ERROR", err.Error(), nil)
		}
		writeMCPRPCError(w, http.StatusInternalServerError, req.ID, -32603, "construct upstream MCP request failed")
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	if accept := strings.TrimSpace(r.Header.Get("Accept")); accept != "" {
		upReq.Header.Set("Accept", accept)
	}
	if sessID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id")); sessID != "" {
		upReq.Header.Set("Mcp-Session-Id", sessID)
	}
	hc := s.opsClient
	if s.agentRegistry != nil && s.agentRegistry.authedClient != nil {
		hc = s.agentRegistry.authedClient
	}
	resp, err := hc.Do(upReq)
	if err != nil {
		if finishMCPSpan != nil {
			finishMCPSpan("ERROR", err.Error(), nil)
		}
		writeMCPRPCError(w, http.StatusBadGateway, req.ID, -32603, "upstream MCP server unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if readErr != nil {
		if finishMCPSpan != nil {
			finishMCPSpan("ERROR", readErr.Error(), nil)
		}
		writeMCPRPCError(w, http.StatusBadGateway, req.ID, -32603, "read upstream MCP response failed")
		return
	}
	if req.Method == "tools/call" {
		s.mcpAllowedCount.Add(1)
		s.metrics.ObserveMCPCall(srv.MCPServerID, "allow")
		s.recordPolicyEvent(PolicyEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Boundary:  "Agent Registry MCP (/mcp/" + srv.MCPServerID + ")",
			Action:    "tools/call (" + call.Name + ")",
			Verdict:   "ALLOW",
			Detail:    fmt.Sprintf("readOnlyHint=%t, destructiveHint=%t verified; proxied to %s", callAnn.ReadOnlyHint, callAnn.DestructiveHint, targetURL),
		})
		if finishMCPSpan != nil {
			st := "OK"
			if resp.StatusCode >= 400 {
				st = "ERROR"
			}
			finishMCPSpan(st, "", map[string]any{"mcp.verdict": "ALLOW"})
		}
	}
	if sessID := resp.Header.Get("Mcp-Session-Id"); sessID != "" {
		w.Header().Set("Mcp-Session-Id", sessID)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBytes)
}

func (s *Server) queryServiceErrorTelemetry(ctx context.Context, service, namespace string, limit int) map[string]any {
	var cloudEntries []map[string]any
	source := "ledger-api-telemetry"

	if !s.cfg.MockLLM && s.agentRegistry != nil && s.agentRegistry.authedClient != nil && s.cfg.ProjectID != "" && s.cfg.ProjectID != "local-dev" {
		logBase := strings.TrimRight(firstNonEmpty(s.cfg.LoggingUpstreamURL, "https://logging.googleapis.com"), "/")
		reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		reqBody, _ := json.Marshal(map[string]any{
			"resourceNames": []string{"projects/" + s.cfg.ProjectID},
			"filter":        fmt.Sprintf(`resource.type="k8s_container" AND resource.labels.namespace_name=%q AND severity>=WARNING`, namespace),
			"orderBy":       "timestamp desc",
			"pageSize":      limit,
		})
		if httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, logBase+"/v2/entries:list", bytes.NewReader(reqBody)); err == nil {
			httpReq.Header.Set("Content-Type", "application/json")
			if resp, err := s.agentRegistry.authedClient.Do(httpReq); err == nil {
				var parsed struct {
					Entries []map[string]any `json:"entries"`
				}
				_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK && len(parsed.Entries) > 0 {
					cloudEntries = parsed.Entries
					source = "cloud-logging-api"
				}
			}
		}
		cancel()
	}

	snap, snapErr := s.fetchLedgerStatus(ctx)
	var syntheticEntries []map[string]any
	if snap != nil {
		if !snap.SLOMet {
			syntheticEntries = append(syntheticEntries,
				map[string]any{
					"severity":              "ERROR",
					"service":               service,
					"namespace":             namespace,
					"event":                 "pool_exhausted",
					"message":               "ledger_db: connection pool exhausted acquiring slot in ReconcileTransfer",
					"http_status":           503,
					"max_open_conns":        snap.MaxOpenConns,
					"in_use_conns":          snap.InUseConns,
					"active_leaked_workers": snap.ActiveLeakedWorkers,
					"idle_timeout_seconds":  snap.IdleTimeoutSeconds,
				},
				map[string]any{
					"severity":              "WARNING",
					"service":               service,
					"namespace":             namespace,
					"event":                 "checksum_mismatch",
					"message":               "rows.ValidateChecksum() failed before rows.Close() in pool/store.go:ReconcileTransfer",
					"http_status":           422,
					"active_leaked_workers": snap.ActiveLeakedWorkers,
				},
			)
		} else {
			syntheticEntries = append(syntheticEntries, map[string]any{
				"severity":              "INFO",
				"service":               service,
				"namespace":             namespace,
				"event":                 "slo_healthy",
				"message":               "ledger_db connection pool healthy; zero leaked reconciliation workers",
				"http_status":           200,
				"max_open_conns":        snap.MaxOpenConns,
				"idle_timeout_seconds":  snap.IdleTimeoutSeconds,
				"active_leaked_workers": snap.ActiveLeakedWorkers,
			})
		}
	}

	entries := cloudEntries
	if len(entries) == 0 {
		entries = syntheticEntries
	}
	out := map[string]any{
		"source":    source,
		"service":   service,
		"namespace": namespace,
		"entries":   entries,
	}
	if snap != nil {
		out["live_status"] = snap
	} else if snapErr != nil {
		out["status_note"] = snapErr.Error()
	}
	return out
}

func (s *Server) inspectDeploymentHealth(ctx context.Context, namespace, deployment string) map[string]any {
	healthzCode := http.StatusServiceUnavailable
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	if req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, s.cfg.LedgerAPIURL+"/healthz", nil); err == nil {
		if resp, err := s.opsClient.Do(req); err == nil {
			healthzCode = resp.StatusCode
			_ = resp.Body.Close()
		}
	}
	cancel()

	snap, _ := s.fetchLedgerStatus(ctx)
	out := map[string]any{
		"namespace":          namespace,
		"deployment":         deployment,
		"healthz_http_code":  healthzCode,
		"ready_replicas":     1,
		"available_replicas": 1,
	}
	if snap != nil {
		out["status"] = snap.Status
		out["slo_met"] = snap.SLOMet
		out["pool_exhausted"] = snap.PoolExhausted
		out["max_open_conns"] = snap.MaxOpenConns
		out["idle_timeout_seconds"] = snap.IdleTimeoutSeconds
		out["active_leaked_workers"] = snap.ActiveLeakedWorkers
		out["p99_latency_ms"] = snap.P99LatencyMs
	}
	if !s.cfg.MockLLM && s.agentRegistry != nil && s.agentRegistry.authedClient != nil && s.cfg.ProjectID != "" && s.cfg.ClusterName != "" {
		gkeBase := strings.TrimRight(firstNonEmpty(s.cfg.GKEUpstreamURL, "https://container.googleapis.com"), "/")
		loc := firstNonEmpty(s.cfg.ClusterLocation, s.cfg.Region, "us-central1")
		cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
		clusterURL := fmt.Sprintf("%s/v1/projects/%s/locations/%s/clusters/%s", gkeBase, s.cfg.ProjectID, loc, s.cfg.ClusterName)
		if req, err := http.NewRequestWithContext(cctx, http.MethodGet, clusterURL, nil); err == nil {
			if resp, err := s.agentRegistry.authedClient.Do(req); err == nil {
				var clusterDoc struct {
					Name   string `json:"name"`
					Status string `json:"status"`
				}
				_ = json.NewDecoder(io.LimitReader(resp.Body, 512<<10)).Decode(&clusterDoc)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK && clusterDoc.Name != "" {
					out["gke_cluster"] = clusterDoc.Name
					out["gke_cluster_status"] = clusterDoc.Status
				}
			}
		}
		ccancel()
	}
	return out
}

func isJSONRPCNotification(req mcpJSONRPCRequest) bool {
	return len(req.ID) == 0 || string(req.ID) == "null"
}

func writeMCPInitializeResult(w http.ResponseWriter, req mcpJSONRPCRequest, serverName string) {
	protoVer := "2025-03-26"
	var initParams struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(req.Params) > 0 && json.Unmarshal(req.Params, &initParams) == nil && initParams.ProtocolVersion != "" {
		protoVer = initParams.ProtocolVersion
	}
	if sessID, err := randomHex(16); err == nil {
		w.Header().Set("Mcp-Session-Id", sessID)
	}
	writeMCPRPCResult(w, req.ID, map[string]any{
		"protocolVersion": protoVer,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": "1.0.0",
		},
	})
}

func writeMCPToolText(w http.ResponseWriter, id json.RawMessage, text string, isErr bool) {
	writeMCPRPCResult(w, id, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"isError": isErr,
	})
}

func writeMCPRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func writeMCPRPCError(w http.ResponseWriter, statusCode int, id json.RawMessage, rpcCode int, message string) {
	writeBrokerJSON(w, statusCode, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    rpcCode,
			"message": message,
		},
	})
}
