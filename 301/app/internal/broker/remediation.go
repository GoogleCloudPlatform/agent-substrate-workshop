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
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/telemetry"
)

const remediationGateTicketTTL = 2 * time.Minute

type approvedGateTicket struct {
	incidentID string
	callID     string
	expiresAt  time.Time
}

// RemediationGateDTO describes a pending human-in-the-loop Remediation Gate
// while the Incident Actor is suspended at 0 vCPU in Cloud Storage.
type RemediationGateDTO struct {
	IncidentID     string         `json:"incident_id"`
	ActorName      string         `json:"actor_name"`
	SessionID      string         `json:"session_id"`
	Template       string         `json:"template,omitempty"`
	CallID         string         `json:"call_id"`
	ToolName       string         `json:"tool_name"`
	Args           map[string]any `json:"args,omitempty"`
	Hint           string         `json:"hint,omitempty"`
	Branch         string         `json:"branch,omitempty"`
	CommitSHA      string         `json:"commit_sha,omitempty"`
	ActorSuspended bool           `json:"actor_suspended"`
	CreatedAt      string         `json:"created_at"`
}

func (s *Server) issueGateApprovalTicket(incidentID, callID string) (string, error) {
	tok, err := randomHex(32)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	s.mu.Lock()
	for k, v := range s.approvedGates {
		if !now.Before(v.expiresAt) {
			delete(s.approvedGates, k)
		}
	}
	s.approvedGates[tok] = approvedGateTicket{
		incidentID: strings.ToUpper(strings.TrimSpace(incidentID)),
		callID:     strings.TrimSpace(callID),
		expiresAt:  now.Add(remediationGateTicketTTL),
	}
	s.mu.Unlock()
	return tok, nil
}

func (s *Server) revokeGateApprovalTicket(tok string) {
	if tok == "" {
		return
	}
	s.mu.Lock()
	delete(s.approvedGates, tok)
	s.mu.Unlock()
}

func (s *Server) consumeGateApprovalTicket(tok, requestedIncidentID string) bool {
	trimmed := strings.TrimSpace(tok)
	if len(trimmed) < 64 {
		return false
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, ok := s.approvedGates[trimmed]
	if !ok {
		return false
	}
	delete(s.approvedGates, trimmed)
	if !now.Before(ticket.expiresAt) {
		return false
	}
	reqInc := strings.ToUpper(strings.TrimSpace(requestedIncidentID))
	if reqInc != "" && ticket.incidentID != "" && !strings.EqualFold(reqInc, ticket.incidentID) {
		return false
	}
	return true
}

// handleIncidentSubroutes serves POST /api/incidents/{id}/remediation/{approve,reject}.
func (s *Server) handleIncidentSubroutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/incidents/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[1] != "remediation" {
		http.NotFound(w, r)
		return
	}
	incID := strings.ToUpper(strings.TrimSpace(parts[0]))
	actorName := incidentToActorName(incID)
	if incID == "" || !validActorLabel.MatchString(actorName) {
		http.NotFound(w, r)
		return
	}

	switch parts[2] {
	case "approve":
		s.handleRemediationApprove(w, r, incID, actorName, true)
	case "reject":
		s.handleRemediationApprove(w, r, incID, actorName, false)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleRemediationApprove(w http.ResponseWriter, r *http.Request, incID, actorName string, confirmed bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if fetchSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); fetchSite != "" &&
		fetchSite != "same-origin" && fetchSite != "none" {
		writeBrokerJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site remediation gate mutation denied"})
		return
	}
	if !s.hasValidAdminToken(r) {
		writeBrokerJSON(w, http.StatusForbidden, map[string]string{"error": "missing or invalid X-Broker-Admin-Token"})
		return
	}

	var body struct {
		CallID    string `json:"call_id"`
		SessionID string `json:"session_id"`
		Template  string `json:"template"`
	}
	if r.Body != nil {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
		if err != nil {
			writeBrokerJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "remediation decision payload too large"})
			return
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid remediation decision JSON"})
				return
			}
		}
	}

	s.mu.Lock()
	gate, hasGate := s.pendingGates[incID]
	s.mu.Unlock()

	callID := firstNonEmpty(body.CallID, gate.CallID)
	if callID == "" {
		writeBrokerJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("no pending Remediation Gate found for %s", incID),
		})
		return
	}
	sessID := firstNonEmpty(body.SessionID, gate.SessionID)
	if sessID == "" {
		sessID = actorName
	}
	tmpl := firstNonEmpty(body.Template, gate.Template)
	if tmpl == "" {
		tmpl = s.cfg.DefaultTemplate
	}

	if confirmed && s.cfg.Require3LO {
		tok, ch, authErr := s.authManager.ResolveForRemediation(r.Context(), s.resolveBaseURL(r), default3LOOperator)
		if authErr != nil {
			writeBrokerJSON(w, http.StatusBadGateway, map[string]string{"error": authErr.Error()})
			return
		}
		if tok == "" {
			s.recordPolicyEvent(PolicyEvent{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Boundary:  "Auth Manager (3LO)",
				Action:    fmt.Sprintf("ApproveRemediation (%s)", incID),
				Verdict:   "CONSENT_REQUIRED",
				Detail:    fmt.Sprintf("Operator 3LO consent required (%s) before waking %s to execute apply_remediation_pr", ch.Provider, actorName),
			})
			writeBrokerJSON(w, http.StatusUnauthorized, map[string]any{
				"consent_required": true,
				"auth_url":         ch.AuthURL,
				"state":            ch.State,
				"provider":         ch.Provider,
				"scopes":           ch.Scopes,
				"error":            "3LO operator consent required before approving remediation",
			})
			return
		}
	}

	var gateToken string
	if confirmed {
		var err error
		gateToken, err = s.issueGateApprovalTicket(incID, callID)
		if err != nil {
			writeBrokerJSON(w, http.StatusInternalServerError, map[string]string{"error": "issue remediation gate approval ticket failed"})
			return
		}
		defer s.revokeGateApprovalTicket(gateToken)
	}

	decisionLabel := "REJECT"
	actionVerb := "RejectRemediation"
	if confirmed {
		decisionLabel = "APPROVE"
		actionVerb = "ApproveRemediation"
	}

	resumeStart := time.Now()
	reqCtx, _, _ := telemetry.ExtractHTTP(r.Context(), r.Header)
	out, statusCode, err := s.executeChatTurn(reqCtx, ChatRequest{
		IncidentID:            incID,
		SessionID:             sessID,
		Template:              tmpl,
		ConfirmationCallID:    callID,
		ConfirmationConfirmed: &confirmed,
		gateApprovalToken:     gateToken,
	})
	totalMs := time.Since(resumeStart).Milliseconds()
	if err != nil {
		s.recordPolicyEvent(PolicyEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Boundary:  "Remediation Gate",
			Action:    fmt.Sprintf("%s (%s)", actionVerb, incID),
			Verdict:   "ERROR",
			Detail:    err.Error(),
		})
		writeBrokerJSON(w, statusCode, map[string]string{"error": err.Error()})
		return
	}

	resumeMs := out.ResumeDurationMs
	if resumeMs < 1 {
		resumeMs = totalMs - out.DurationMs
		if resumeMs < 1 {
			resumeMs = 1
		}
		out.ResumeDurationMs = resumeMs
	}
	detail := fmt.Sprintf("Resumed %s from Cloud Storage in %dms (%s call_id=%s)", actorName, resumeMs, decisionLabel, callID)
	if hasGate && gate.Branch != "" {
		detail = fmt.Sprintf("Resumed %s from Cloud Storage in %dms (%s branch=%s call_id=%s)", actorName, resumeMs, decisionLabel, gate.Branch, callID)
	}
	s.recordPolicyEvent(PolicyEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Boundary:  "Remediation Gate",
		Action:    fmt.Sprintf("%s (%s)", actionVerb, incID),
		Verdict:   decisionLabel,
		Detail:    detail,
	})

	writeBrokerJSON(w, http.StatusOK, out)
}
