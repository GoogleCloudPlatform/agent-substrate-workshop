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

// Package agentruntime hosts the HTTP server and ADK Go v2 turn execution loop
// running on port 80 inside each keyless Agent Substrate Incident Actor.
package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/brokerclient"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/telemetry"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

const (
	// AppName identifies the Platform SRE Triage Agent across sessions and memories.
	AppName = "substrate-sre-triage"

	// DefaultInstruction guides the ADK Go v2 LlmAgent inside the Incident Actor.
	DefaultInstruction = `You are the Platform SRE Triage Agent running inside an isolated Agent Substrate gVisor Incident Actor on Google Kubernetes Engine.
Investigate and remediate production incidents systematically:
1. When MCP observability tools are available (query_service_error_logs, get_deployment_health, get_service_slo), query live error telemetry and workload health first.
2. Clone the Target Service repository into /workspace/ledger-api with clone_repo, execute any relevant Procedural Skill runbooks via load_skill (unpacked scripts live under /workspace/.skills/ and run via run_workspace_command with command="sh", args=[".skills/<skill>/scripts/<script>.sh"], dir="ledger-api"), and inspect config/pool.yaml and pool/store.go with read_workspace_file.
3. Run ledger-verify via run_workspace_command to confirm configuration bounds (max_open_conns between 16 and 256, idle_timeout_seconds >= 5) and AST cursor-release invariants (defer rows.Close() before ValidateChecksum in ReconcileTransfer).
4. When asked to stage and remediate an incident, call stage_git_patch directly on branch fix/inc-402-pool-leak (stage_git_patch runs ledger-verify automatically), then immediately call push_git_branch and apply_remediation_pr without extra git log/diff/status detours so human operators can review and approve at the Remediation Gate.`

	actorCSP   = "default-src 'none'; frame-ancestors 'none'"
	maxTurnLen = 256 << 10
)

// Config configures an Incident Actor Server instance.
type Config struct {
	WorkspaceDir string
	BrokerURL    string
	ModelName    string
	TemplateName string
	BuildRunner  func(ctx context.Context, cfg Config, workspaceTools []tool.Tool) (*runner.Runner, error)
}

// ToolStep records one ADK tool invocation observed during a turn.
type ToolStep struct {
	Name       string `json:"name"`
	Args       string `json:"args,omitempty"`
	Result     string `json:"result,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// TurnRequest is sent by the Governed Broker over atenet-router to POST /turn.
type TurnRequest struct {
	IncidentID            string         `json:"incident_id"`
	SessionID             string         `json:"session_id"`
	UserID                string         `json:"user_id"`
	Prompt                string         `json:"prompt"`
	IncidentContext       string         `json:"incident_context,omitempty"`
	ConfirmationCallID    string         `json:"confirmation_call_id,omitempty"`
	ConfirmationConfirmed *bool          `json:"confirmation_confirmed,omitempty"`
	GateApprovalToken     string         `json:"gate_approval_token,omitempty"`
	StateDelta            map[string]any `json:"state_delta,omitempty"`
}

// TurnResponse summarizes the completed or gated ADK turn from the Incident Actor.
type TurnResponse struct {
	IncidentID             string         `json:"incident_id"`
	SessionID              string         `json:"session_id"`
	Template               string         `json:"template"`
	Reply                  string         `json:"reply"`
	Backend                string         `json:"backend"`
	StarterStub            bool           `json:"starter_stub,omitempty"`
	ToolSteps              []ToolStep     `json:"tool_steps"`
	Workspace              WorkspaceState `json:"workspace"`
	RemediationGatePending bool           `json:"remediation_gate_pending"`
	ConfirmationCallID     string         `json:"confirmation_call_id,omitempty"`
	CallID                 string         `json:"call_id,omitempty"`
	ToolName               string         `json:"tool_name,omitempty"`
	ToolArgs               map[string]any `json:"args,omitempty"`
	ConfirmationHint       string         `json:"hint,omitempty"`
	TraceID                string         `json:"trace_id,omitempty"`
	DurationMs             int64          `json:"duration_ms"`
}

// Server serves /readyz, /turn, and /workspace/state inside an Incident Actor.
type Server struct {
	cfg        Config
	workspace  *Workspace
	wsTools    []tool.Tool
	runner     *runner.Runner
	otlpClient *http.Client

	turnMu    sync.Mutex
	stepLogMu sync.Mutex
	turnSteps []ToolStep
}

// NewServer initializes the workspace tools and prepares lazy ADK Runner construction.
// BuildRunner runs on the first POST /turn after an Incident Actor wakes with its
// EgressPolicy active; Golden Snapshot warming runs with default-deny egress.
func NewServer(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.WorkspaceDir) == "" {
		cfg.WorkspaceDir = "/workspace"
	}
	if strings.TrimSpace(cfg.ModelName) == "" {
		cfg.ModelName = brokerclient.DefaultModelName
	}
	if strings.TrimSpace(cfg.TemplateName) == "" {
		cfg.TemplateName = "sre-agent-v1"
	}

	telemetry.InstallADKTracerProvider()

	ws, err := NewWorkspace(cfg.WorkspaceDir, cfg.BrokerURL)
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:        cfg,
		workspace:  ws,
		otlpClient: &http.Client{Timeout: 5 * time.Second},
	}
	wsTools, err := NewWorkspaceTools(ws, s.recordStep)
	if err != nil {
		return nil, fmt.Errorf("create workspace tools: %w", err)
	}
	s.wsTools = wsTools
	return s, nil
}

func (s *Server) recordStep(st ToolStep) {
	s.stepLogMu.Lock()
	defer s.stepLogMu.Unlock()
	s.turnSteps = append(s.turnSteps, st)
}

func (s *Server) resetSteps() {
	s.stepLogMu.Lock()
	defer s.stepLogMu.Unlock()
	s.turnSteps = nil
}

func (s *Server) drainSteps() []ToolStep {
	s.stepLogMu.Lock()
	defer s.stepLogMu.Unlock()
	out := make([]ToolStep, len(s.turnSteps))
	copy(out, s.turnSteps)
	s.turnSteps = nil
	return out
}

// Handler returns the HTTP handler serving /readyz, /turn, and /workspace/state.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/healthz", s.handleReadyz)
	mux.HandleFunc("/turn", s.handleTurn)
	mux.HandleFunc("/workspace/state", s.handleWorkspaceState)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", actorCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, HEAD, POST")
			writeActorJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeActorJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeActorJSON(w, http.StatusOK, map[string]any{
		"status":         "ready",
		"template":       s.cfg.TemplateName,
		"runner_enabled": s.cfg.BuildRunner != nil,
	})
}

func (s *Server) handleWorkspaceState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeActorJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeActorJSON(w, http.StatusOK, s.workspace.Inspect(r.Context()))
}

func (s *Server) handleTurn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeActorJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req TurnRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTurnLen))
	if err := dec.Decode(&req); err != nil {
		writeActorJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid turn request JSON"})
		return
	}

	incID := strings.ToUpper(strings.TrimSpace(req.IncidentID))
	if incID == "" {
		incID = "INC-402"
	}
	sessID := strings.TrimSpace(req.SessionID)
	if sessID == "" {
		sessID = strings.ToLower(incID)
	}
	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		userID = "sre-operator"
	}

	start := time.Now()
	actorID := strings.ReplaceAll(strings.ToLower(incID), "_", "-")
	traceCtx, incomingSC, hasIncoming := telemetry.ExtractHTTP(r.Context(), r.Header)
	if !hasIncoming {
		incomingSC = telemetry.SpanContext{
			TraceID: telemetry.NewTraceID(),
			SpanID:  telemetry.NewSpanID(),
			Flags:   "01",
		}
		traceCtx = telemetry.ContextWithSpanContext(traceCtx, incomingSC)
	}
	parentSpanID := ""
	if hasIncoming {
		parentSpanID = incomingSC.SpanID
	}
	w.Header().Set("traceparent", incomingSC.Traceparent())

	spanBuf := telemetry.NewSpanBuffer("sre-agent", telemetry.StampContext{
		AgentName: telemetry.DefaultAgentName,
		SessionID: sessID,
		ActorID:   actorID,
		Template:  s.cfg.TemplateName,
		ModelName: s.cfg.ModelName,
	})
	turnCtx := telemetry.ContextWithBuffer(traceCtx, spanBuf)
	turnCtx = brokerclient.ContextWithGateApprovalToken(turnCtx, req.GateApprovalToken)

	s.turnMu.Lock()
	defer s.turnMu.Unlock()

	if s.runner == nil && s.cfg.BuildRunner != nil {
		built, buildErr := s.cfg.BuildRunner(turnCtx, s.cfg, s.wsTools)
		if buildErr != nil {
			initErr := fmt.Errorf("build ADK runner: %w", buildErr)
			wsSnap := s.workspace.Inspect(r.Context())
			collected := finalizeActorSpans(spanBuf.Drain(), incomingSC.TraceID, parentSpanID, incID, nil, start, time.Now(), initErr)
			_ = telemetry.PostOTLPSpans(r.Context(), s.otlpClient, s.cfg.BrokerURL, "sre-agent", collected)
			writeActorJSON(w, http.StatusBadGateway, map[string]any{
				"error":       initErr.Error(),
				"incident_id": incID,
				"session_id":  sessID,
				"trace_id":    incomingSC.TraceID,
				"workspace":   wsSnap,
			})
			return
		}
		s.runner = built
	}

	if s.runner == nil {
		end := time.Now()
		spanBuf.Add(telemetry.NewSpanRecord(
			incomingSC.TraceID,
			telemetry.NewSpanID(),
			parentSpanID,
			"invoke_agent",
			"SERVER",
			"Incident Actor",
			"sre-agent",
			start,
			end,
			"OK",
			"",
			map[string]any{
				"gen_ai.operation.name": "invoke_agent",
				"incident.id":           incID,
				"starter_stub":          true,
			},
		))
		_ = telemetry.PostOTLPSpans(r.Context(), s.otlpClient, s.cfg.BrokerURL, "sre-agent", spanBuf.Drain())
		writeActorJSON(w, http.StatusOK, TurnResponse{
			IncidentID:  incID,
			SessionID:   sessID,
			Template:    s.cfg.TemplateName,
			StarterStub: true,
			Backend:     "starter-stub",
			Reply: "Incident Actor " + strings.ToLower(incID) + " is awake on template " + s.cfg.TemplateName +
				", and the ADK Go v2 Runner is not wired yet. Complete WORKSHOP(module-1) in app/cmd/agent/main.go, run `make build-agent`, and recreate template sre-agent-v1.",
			ToolSteps:  []ToolStep{},
			Workspace:  s.workspace.Inspect(r.Context()),
			TraceID:    incomingSC.TraceID,
			DurationMs: end.Sub(start).Milliseconds(),
		})
		return
	}

	s.resetSteps()

	msg := buildTurnContent(req, incID)
	var runOpts []runner.RunOption
	if len(req.StateDelta) > 0 {
		runOpts = append(runOpts, runner.WithStateDelta(req.StateDelta))
	}

	var (
		replyParts      []string
		gatePending     bool
		confirmCallID   string
		confirmToolName string
		confirmToolArgs map[string]any
		confirmHint     string
		runnerTurnError error
		pendingADKCalls = make(map[string]struct {
			name  string
			args  string
			start time.Time
		})
	)

	isWorkspaceTool := func(name string) bool {
		switch name {
		case "clone_repo", "read_workspace_file", "run_workspace_command", "stage_git_patch", toolconfirmation.FunctionCallName:
			return true
		default:
			return false
		}
	}

	for ev, err := range s.runner.Run(turnCtx, userID, sessID, msg, agent.RunConfig{}, runOpts...) {
		if err != nil {
			runnerTurnError = err
			break
		}
		if ev == nil || ev.Content == nil {
			continue
		}
		for _, part := range ev.Content.Parts {
			if part == nil {
				continue
			}
			if fc := part.FunctionCall; fc != nil {
				if fc.Name == toolconfirmation.FunctionCallName {
					gatePending = true
					confirmCallID = fc.ID
					if orig, origErr := toolconfirmation.OriginalCallFrom(fc); origErr == nil && orig != nil {
						confirmToolName = orig.Name
						confirmToolArgs = orig.Args
					}
					confirmHint = extractConfirmationHint(fc.Args)
				} else if !isWorkspaceTool(fc.Name) {
					argsRaw, _ := json.Marshal(fc.Args)
					key := firstNonEmptyKey(fc.ID, fc.Name)
					pendingADKCalls[key] = struct {
						name  string
						args  string
						start time.Time
					}{
						name:  fc.Name,
						args:  string(argsRaw),
						start: time.Now(),
					}
				}
			}
			if fr := part.FunctionResponse; fr != nil && !isWorkspaceTool(fr.Name) {
				key := firstNonEmptyKey(fr.ID, fr.Name)
				callMeta, ok := pendingADKCalls[key]
				if !ok {
					callMeta.name = fr.Name
					callMeta.start = time.Now()
				}
				delete(pendingADKCalls, key)
				respBytes, _ := json.Marshal(fr.Response)
				resStr := string(respBytes)
				if len(resStr) > 2048 {
					resStr = resStr[:2048] + "...(truncated)"
				}
				s.recordStep(ToolStep{
					Name:       fr.Name,
					Args:       callMeta.args,
					Result:     resStr,
					DurationMs: time.Since(callMeta.start).Milliseconds(),
				})
			}
			if part.Text != "" && ev.Content.Role == genai.RoleModel {
				replyParts = append(replyParts, strings.TrimSpace(part.Text))
			}
		}
	}

	steps := s.drainSteps()
	wsSnap := s.workspace.Inspect(r.Context())
	collected := finalizeActorSpans(spanBuf.Drain(), incomingSC.TraceID, parentSpanID, incID, steps, start, time.Now(), runnerTurnError)
	_ = telemetry.PostOTLPSpans(r.Context(), s.otlpClient, s.cfg.BrokerURL, "sre-agent", collected)

	if runnerTurnError != nil {
		writeActorJSON(w, http.StatusBadGateway, map[string]any{
			"error":       runnerTurnError.Error(),
			"incident_id": incID,
			"session_id":  sessID,
			"trace_id":    incomingSC.TraceID,
			"tool_steps":  steps,
			"workspace":   wsSnap,
		})
		return
	}

	reply := strings.TrimSpace(strings.Join(replyParts, "\n\n"))
	if gatePending && reply == "" {
		reply = "Paused at the Remediation Gate awaiting operator confirmation before applying production changes."
	}

	writeActorJSON(w, http.StatusOK, TurnResponse{
		IncidentID:             incID,
		SessionID:              sessID,
		Template:               s.cfg.TemplateName,
		Reply:                  reply,
		Backend:                "adk-gemini",
		ToolSteps:              steps,
		Workspace:              wsSnap,
		RemediationGatePending: gatePending,
		ConfirmationCallID:     confirmCallID,
		CallID:                 confirmCallID,
		ToolName:               confirmToolName,
		ToolArgs:               confirmToolArgs,
		ConfirmationHint:       confirmHint,
		TraceID:                incomingSC.TraceID,
		DurationMs:             time.Since(start).Milliseconds(),
	})
}

func finalizeActorSpans(
	spans []telemetry.SpanRecord,
	traceID, parentSpanID, incID string,
	steps []ToolStep,
	start, end time.Time,
	turnErr error,
) []telemetry.SpanRecord {
	var invokeSpanID string
	tracedTools := map[string]bool{}
	for i := range spans {
		if spans[i].Attributes == nil {
			spans[i].Attributes = map[string]any{}
		}
		spans[i].Attributes["incident.id"] = incID
		if spans[i].Name == "invoke_agent" && invokeSpanID == "" {
			invokeSpanID = spans[i].SpanID
			if spans[i].ParentSpanID == "" && parentSpanID != "" {
				spans[i].ParentSpanID = parentSpanID
			}
		}
		if spans[i].Name == "execute_tool" {
			if tn, ok := spans[i].Attributes["gen_ai.tool.name"].(string); ok && tn != "" {
				tracedTools[tn] = true
			}
		}
	}
	if invokeSpanID == "" {
		invokeSpanID = telemetry.NewSpanID()
		status := "OK"
		msg := ""
		if turnErr != nil {
			status = "ERROR"
			msg = turnErr.Error()
		}
		spans = append([]telemetry.SpanRecord{
			telemetry.NewSpanRecord(
				traceID,
				invokeSpanID,
				parentSpanID,
				"invoke_agent",
				"SERVER",
				"Incident Actor",
				"sre-agent",
				start,
				end,
				status,
				msg,
				map[string]any{
					"gen_ai.operation.name": "invoke_agent",
					"incident.id":           incID,
				},
			),
		}, spans...)
	}
	cursor := start
	for _, st := range steps {
		if st.Name == "" || tracedTools[st.Name] {
			continue
		}
		dur := time.Duration(st.DurationMs) * time.Millisecond
		if dur <= 0 {
			dur = 250 * time.Microsecond
		}
		stepEnd := cursor.Add(dur)
		if stepEnd.After(end) {
			stepEnd = end
		}
		spans = append(spans, telemetry.NewSpanRecord(
			traceID,
			telemetry.NewSpanID(),
			invokeSpanID,
			"execute_tool",
			"INTERNAL",
			"Incident Actor",
			"sre-agent",
			cursor,
			stepEnd,
			"OK",
			"",
			map[string]any{
				"gen_ai.operation.name": "execute_tool",
				"gen_ai.tool.name":      st.Name,
				"incident.id":           incID,
			},
		))
		cursor = stepEnd
	}
	return spans
}

func extractConfirmationHint(args map[string]any) string {
	if args == nil {
		return ""
	}
	switch v := args["toolConfirmation"].(type) {
	case toolconfirmation.ToolConfirmation:
		return strings.TrimSpace(v.Hint)
	case *toolconfirmation.ToolConfirmation:
		if v != nil {
			return strings.TrimSpace(v.Hint)
		}
	case map[string]any:
		if h, ok := v["hint"].(string); ok {
			return strings.TrimSpace(h)
		}
	}
	return "Approve or reject production remediation before applying changes."
}

func buildTurnContent(req TurnRequest, incID string) *genai.Content {
	if strings.TrimSpace(req.ConfirmationCallID) != "" && req.ConfirmationConfirmed != nil {
		part := genai.NewPartFromFunctionResponse(toolconfirmation.FunctionCallName, map[string]any{
			"confirmed": *req.ConfirmationConfirmed,
		})
		part.FunctionResponse.ID = strings.TrimSpace(req.ConfirmationCallID)
		return &genai.Content{
			Role:  genai.RoleUser,
			Parts: []*genai.Part{part},
		}
	}
	text := strings.TrimSpace(req.Prompt)
	if text == "" {
		text = "Investigate " + incID + " by cloning ledger-api into /workspace, inspecting config/pool.yaml and pool/store.go, running ledger-verify, and reporting the root cause."
	}
	if ctxNote := strings.TrimSpace(req.IncidentContext); ctxNote != "" {
		text = fmt.Sprintf("[Incident %s context]\n%s\n\n[Operator request]\n%s", incID, ctxNote, text)
	}
	return genai.NewContentFromText(text, genai.RoleUser)
}

func firstNonEmptyKey(id, fallback string) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	return fallback
}

func writeActorJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
