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
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/telemetry"
)

const (
	a2aAgentID          = "sre-triage-agent"
	a2aAgentDisplayName = "Platform SRE Triage Agent"
	a2aAgentVersion     = "0.4.0"
	a2aProtocolVersion  = "0.3.0"
)

var incidentIDInTextPattern = regexp.MustCompile(`(?i)\bINC-[0-9]{2,5}\b`)

// A2AInterfaceDTO describes one supported A2A transport endpoint on the Agent Card.
type A2AInterfaceDTO struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	ProtocolVersion string `json:"protocolVersion"`
}

// A2ACapabilitiesDTO declares the capabilities supported by the A2A endpoint.
type A2ACapabilitiesDTO struct {
	Streaming              bool `json:"streaming"`
	PushNotifications      bool `json:"pushNotifications"`
	StateTransitionHistory bool `json:"stateTransitionHistory"`
}

// A2ASkillDTO describes an operational capability advertised in the A2A Agent Card.
type A2ASkillDTO struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// A2AAgentCardDTO is the JSON document served at GET /.well-known/agent-card.json
// and embedded inside Agent Registry A2A agent descriptors.
type A2AAgentCardDTO struct {
	Name                string             `json:"name"`
	Description         string             `json:"description"`
	Version             string             `json:"version"`
	ProtocolVersion     string             `json:"protocolVersion"`
	URL                 string             `json:"url"`
	SupportedInterfaces []A2AInterfaceDTO  `json:"supportedInterfaces"`
	DefaultInputModes   []string           `json:"defaultInputModes"`
	DefaultOutputModes  []string           `json:"defaultOutputModes"`
	Capabilities        A2ACapabilitiesDTO `json:"capabilities"`
	Skills              []A2ASkillDTO      `json:"skills"`
}

// A2ATaskPart represents a text or structured data part inside an A2A Task result.
type A2ATaskPart struct {
	Type     string         `json:"type,omitempty"`
	Kind     string         `json:"kind,omitempty"`
	MimeType string         `json:"mimeType,omitempty"`
	Text     string         `json:"text,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

// A2ATaskStatus reports the lifecycle state of an A2A Task.
type A2ATaskStatus struct {
	State     string `json:"state"`
	Timestamp string `json:"timestamp"`
}

// A2ATaskRecord is stored in memory by sre-gateway for POST /a2a message/send and tasks/get.
type A2ATaskRecord struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId"`
	SessionID string         `json:"sessionId"`
	Status    A2ATaskStatus  `json:"status"`
	Parts     []A2ATaskPart  `json:"parts,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

func (s *Server) resolveBaseURL(r *http.Request) string {
	if r != nil && strings.TrimSpace(r.Host) != "" {
		scheme := "http"
		if isHTTPSRequest(r) {
			scheme = "https"
		}
		return scheme + "://" + strings.TrimSpace(r.Host)
	}
	if base := strings.TrimRight(strings.TrimSpace(s.cfg.SelfURL), "/"); base != "" {
		return base
	}
	return fmt.Sprintf("http://sre-gateway.%s.svc.cluster.local:8080", s.cfg.Namespace)
}

func (s *Server) buildAgentCard(baseURL string) A2AAgentCardDTO {
	a2aURL := strings.TrimRight(baseURL, "/") + "/a2a"
	return A2AAgentCardDTO{
		Name:            a2aAgentDisplayName,
		Description:     "Stateful ADK Go v2 incident triage and workspace remediation agent running inside a keyless Agent Substrate gVisor Incident Actor on GKE, governed across the Actor Boundary by Gemini Enterprise Agent Platform.",
		Version:         a2aAgentVersion,
		ProtocolVersion: a2aProtocolVersion,
		URL:             a2aURL,
		SupportedInterfaces: []A2AInterfaceDTO{
			{
				URL:             a2aURL,
				ProtocolBinding: "JSONRPC",
				ProtocolVersion: a2aProtocolVersion,
			},
		},
		DefaultInputModes:  []string{"text", "text/plain", "application/json"},
		DefaultOutputModes: []string{"text", "text/plain", "application/json"},
		Capabilities: A2ACapabilitiesDTO{
			Streaming:              false,
			PushNotifications:      false,
			StateTransitionHistory: true,
		},
		Skills: defaultA2ASkills(),
	}
}

func defaultA2ASkills() []A2ASkillDTO {
	return []A2ASkillDTO{
		{
			ID:          "incident-triage",
			Name:        "Stateful incident triage",
			Description: "Clones Target Service repositories into /workspace, executes SHA-256-verified Procedural Skills, correlates Cloud Logging and GKE MCP telemetry, and recalls Consolidated Memory.",
			Tags:        []string{"sre", "gke", "triage", "mcp", "skills"},
			Examples: []string{
				"Investigate INC-402 by querying MCP logs and inspecting /workspace/ledger-api.",
				"Triage sibling incident INC-403 using Consolidated Memory.",
			},
			InputModes:  []string{"text", "text/plain"},
			OutputModes: []string{"text", "text/plain"},
		},
		{
			ID:          "gated-remediation",
			Name:        "Zero-compute gated remediation",
			Description: "Stages and verifies git fixes inside gVisor with ledger-verify, pushes remediation branches to Operations Hub, and suspends to 0 vCPU in Cloud Storage at the Remediation Gate awaiting operator approval.",
			Tags:        []string{"remediation", "hitl", "substrate", "zero-compute"},
			Examples: []string{
				"Stage and remediate INC-402 on branch fix/inc-402-pool-leak.",
			},
			InputModes:  []string{"text", "text/plain"},
			OutputModes: []string{"text", "text/plain", "application/json"},
		},
	}
}

func (s *Server) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	card := s.buildAgentCard(s.resolveBaseURL(r))
	writeBrokerJSON(w, http.StatusOK, card)
}

func (s *Server) handleA2A(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	var rpcReq struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAPIBody))
	if err := dec.Decode(&rpcReq); err != nil {
		writeA2ARPCError(w, http.StatusBadRequest, json.RawMessage("null"), -32700, "invalid JSON-RPC payload")
		return
	}
	idRaw := rpcReq.ID
	if len(idRaw) == 0 {
		idRaw = json.RawMessage("null")
	}

	switch rpcReq.Method {
	case "message/send", "tasks/send":
		s.handleA2AMessageSend(w, r, idRaw, rpcReq.Params)
	case "tasks/get":
		s.handleA2ATasksGet(w, idRaw, rpcReq.Params)
	default:
		writeA2ARPCError(w, http.StatusBadRequest, idRaw, -32601, fmt.Sprintf("unsupported A2A method %q", rpcReq.Method))
	}
}

func (s *Server) handleA2AMessageSend(w http.ResponseWriter, r *http.Request, idRaw json.RawMessage, rawParams json.RawMessage) {
	var params struct {
		ID        string         `json:"id"`
		SessionID string         `json:"sessionId"`
		ContextID string         `json:"contextId"`
		Metadata  map[string]any `json:"metadata"`
		Message   struct {
			Role  string        `json:"role"`
			Parts []A2ATaskPart `json:"parts"`
		} `json:"message"`
	}
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			writeA2ARPCError(w, http.StatusBadRequest, idRaw, -32602, "invalid message/send params")
			return
		}
	}

	promptText := extractA2APromptText(params.Message.Parts)
	incID := ""
	tmpl := ""
	if params.Metadata != nil {
		if v, ok := params.Metadata["incident_id"].(string); ok {
			incID = strings.ToUpper(strings.TrimSpace(v))
		}
		if v, ok := params.Metadata["template"].(string); ok {
			tmpl = strings.TrimSpace(v)
		}
	}
	if incID == "" {
		if match := incidentIDInTextPattern.FindString(promptText); match != "" {
			incID = strings.ToUpper(match)
		}
	}
	if incID == "" {
		incID = "INC-402"
	}

	sessID := firstNonEmpty(params.SessionID, params.ContextID, incidentToActorName(incID))
	reqCtx, _, _ := telemetry.ExtractHTTP(r.Context(), r.Header)
	chatResp, statusCode, err := s.executeChatTurn(reqCtx, ChatRequest{
		IncidentID: incID,
		SessionID:  sessID,
		Prompt:     promptText,
		Template:   tmpl,
	})
	if err != nil {
		writeA2ARPCError(w, statusCode, idRaw, -32603, err.Error())
		return
	}

	taskID := strings.TrimSpace(params.ID)
	if taskID == "" {
		randSuffix, _ := randomHex(6)
		taskID = "task-" + sessID + "-" + firstNonEmpty(randSuffix, "1")
	}
	stateStr := "completed"
	if chatResp.RemediationGatePending {
		stateStr = "input-required"
	}

	task := A2ATaskRecord{
		ID:        taskID,
		ContextID: firstNonEmpty(params.ContextID, sessID),
		SessionID: chatResp.SessionID,
		Status: A2ATaskStatus{
			State:     stateStr,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
		Parts: []A2ATaskPart{
			{
				Type: "text",
				Kind: "text",
				Text: chatResp.Reply,
			},
			{
				Type:     "data",
				Kind:     "data",
				MimeType: "application/json",
				Data: map[string]any{
					"incident_id":              chatResp.IncidentID,
					"actor_name":               chatResp.ActorName,
					"template":                 chatResp.Template,
					"remediation_gate_pending": chatResp.RemediationGatePending,
					"confirmation_call_id":     chatResp.ConfirmationCallID,
				},
			},
		},
		Metadata: map[string]any{
			"incident_id":              chatResp.IncidentID,
			"actor_name":               chatResp.ActorName,
			"template":                 chatResp.Template,
			"remediation_gate_pending": chatResp.RemediationGatePending,
			"confirmation_call_id":     chatResp.ConfirmationCallID,
		},
	}

	s.mu.Lock()
	if len(s.a2aTasks) >= 256 {
		clear(s.a2aTasks)
	}
	s.a2aTasks[task.ID] = task
	s.mu.Unlock()

	s.recordPolicyEvent(PolicyEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Boundary:  "Agent Registry A2A (/a2a)",
		Action:    "message/send (" + incID + ")",
		Verdict:   "ALLOW",
		Detail:    fmt.Sprintf("Delegated A2A task %s to Incident Actor %s (state=%s)", task.ID, chatResp.ActorName, stateStr),
	})

	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"jsonrpc": "2.0",
		"id":      idRaw,
		"result":  task,
	})
}

func (s *Server) handleA2ATasksGet(w http.ResponseWriter, idRaw json.RawMessage, rawParams json.RawMessage) {
	var params struct {
		ID string `json:"id"`
	}
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			writeA2ARPCError(w, http.StatusBadRequest, idRaw, -32602, "invalid tasks/get params")
			return
		}
	}
	taskID := strings.TrimSpace(params.ID)
	if taskID == "" {
		writeA2ARPCError(w, http.StatusBadRequest, idRaw, -32602, "params.id is required")
		return
	}

	s.mu.Lock()
	task, ok := s.a2aTasks[taskID]
	s.mu.Unlock()
	if !ok {
		writeA2ARPCError(w, http.StatusNotFound, idRaw, -32001, fmt.Sprintf("task %q not found", taskID))
		return
	}

	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"jsonrpc": "2.0",
		"id":      idRaw,
		"result":  task,
	})
}

func extractA2APromptText(parts []A2ATaskPart) string {
	var out []string
	var prev string
	for _, p := range parts {
		isText := p.Type == "text" || p.Type == "text/plain" || p.Kind == "text" || (p.Type == "" && p.Kind == "" && p.Text != "")
		if !isText {
			continue
		}
		trimmed := strings.TrimSpace(p.Text)
		if trimmed == "" || trimmed == prev {
			continue
		}
		out = append(out, trimmed)
		prev = trimmed
	}
	return strings.Join(out, "\n")
}

func writeA2ARPCError(w http.ResponseWriter, httpCode int, id json.RawMessage, rpcCode int, msg string) {
	writeBrokerJSON(w, httpCode, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    rpcCode,
			"message": msg,
		},
	})
}
