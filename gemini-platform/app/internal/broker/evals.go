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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/agentruntime"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/evals"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/telemetry"
)

func (s *Server) handleEvals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s.mu.Lock()
	cached := s.latestEvalReport
	s.mu.Unlock()
	if cached != nil {
		writeBrokerJSON(w, http.StatusOK, cached)
		return
	}
	snap := s.buildTrajectorySnapshot(r.Context())
	preview := s.evaluator.Preview(snap, evals.DefaultThreshold)
	writeBrokerJSON(w, http.StatusOK, preview)
}

func (s *Server) handleRunEvals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.hasValidAdminToken(r) {
		writeBrokerJSON(w, http.StatusForbidden, map[string]string{"error": "missing or invalid X-Broker-Admin-Token"})
		return
	}

	threshold := evals.DefaultThreshold
	if r.Body != nil {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
		if err != nil {
			writeBrokerJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "eval payload too large"})
			return
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			var body struct {
				Threshold float64 `json:"threshold"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid eval request JSON"})
				return
			}
			if body.Threshold > 0 && body.Threshold <= 1.0 {
				threshold = body.Threshold
			}
		}
	}

	reqCtx, _, _ := telemetry.ExtractHTTP(r.Context(), r.Header)
	evalCtx, _, finishSpan := s.startBrokerSpan(reqCtx, "online_evals.evaluate", "INTERNAL", "Online Evals", map[string]any{
		"gen_ai.operation.name": "evaluate_trajectory",
		"gen_ai.request.model":  s.cfg.GeminiModel,
		"eval.threshold":        threshold,
	})

	snap := s.buildTrajectorySnapshot(evalCtx)
	report, err := s.evaluator.Evaluate(evalCtx, snap, threshold)
	if err != nil {
		finishSpan("ERROR", err.Error(), nil)
		writeBrokerJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	scoresMap := make(map[string]float64, len(report.Rubrics))
	for _, rub := range report.Rubrics {
		scoresMap[rub.ID] = rub.Score
	}
	s.metrics.ObserveEvalRun(scoresMap, report.Passed)

	s.mu.Lock()
	repCopy := report
	s.latestEvalReport = &repCopy
	s.mu.Unlock()

	verdict := "FAIL"
	if report.Passed {
		verdict = "PASS"
	}
	finishSpan("OK", "", map[string]any{
		"eval.verdict":       verdict,
		"eval.overall_score": report.OverallScore,
		"eval.judge_backend": report.JudgeBackend,
	})
	s.recordPolicyEvent(PolicyEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Boundary:  "Online Evals",
		Action:    "EvaluateTrajectory (4 rubrics)",
		Verdict:   verdict,
		Detail:    fmt.Sprintf("overall=%.2f threshold=%.2f judge=%s digest=%s", report.OverallScore, report.Threshold, report.JudgeBackend, report.TrajectoryDigest),
	})

	writeBrokerJSON(w, http.StatusOK, report)
}

func (s *Server) buildTrajectorySnapshot(ctx context.Context) evals.TrajectorySnapshot {
	actorsByName := s.actorSummariesMap(ctx)
	ledgerList := s.agentEngine.ListLedgerSessions(actorsByName, s.cfg.DefaultTemplate)

	sessions := make([]evals.SessionEvidence, 0, len(ledgerList))
	var inspectionBlob strings.Builder

	for _, ls := range ledgerList {
		ev := evals.SessionEvidence{
			SessionID:  ls.SessionID,
			IncidentID: ls.IncidentID,
			ActorState: ls.ActorState,
			Template:   ls.Template,
			EventCount: ls.EventCount,
		}
		if detail, ok := s.agentEngine.GetLedgerSessionDetail(ls.SessionID, actorsByName, s.cfg.DefaultTemplate); ok {
			ev.EventCount = len(detail.Events)
			for _, item := range detail.Events {
				for _, tc := range item.ToolCalls {
					if strings.TrimSpace(tc.Name) != "" {
						ev.ToolCalls = append(ev.ToolCalls, tc.Name)
					}
				}
				if strings.TrimSpace(item.Summary) != "" {
					ev.Summaries = append(ev.Summaries, item.Summary)
					inspectionBlob.WriteString(item.Summary + "\n")
				}
				if strings.TrimSpace(item.Text) != "" {
					ev.Summaries = append(ev.Summaries, truncateText(item.Text, 360))
					inspectionBlob.WriteString(item.Text + "\n")
				}
			}
		}
		sessions = append(sessions, ev)
	}

	memories := s.agentEngine.searchMemories(ctx, agentruntime.AppName, "sre-operator", "")
	if len(memories) == 0 {
		memories = s.agentEngine.searchMemories(ctx, agentruntime.AppName, "", "")
	}
	memFacts := make([]string, 0, len(memories))
	for _, m := range memories {
		if strings.TrimSpace(m.Fact) != "" {
			memFacts = append(memFacts, m.Fact)
			inspectionBlob.WriteString(m.Fact + "\n")
		}
	}

	var (
		skillName    string
		skillDefault string
		skillRevs    []string
		skillSHA     string
	)
	for _, sk := range s.skillRegistry.listSummaries() {
		if sk.Name == defaultSkillName || skillName == "" {
			skillName = sk.Name
			skillDefault = sk.DefaultRevision
			skillSHA = sk.ActiveSHA256
			skillRevs = make([]string, 0, len(sk.Revisions))
			for _, r := range sk.Revisions {
				skillRevs = append(skillRevs, r.Revision)
			}
			if sk.Name == defaultSkillName {
				break
			}
		}
	}

	traceList := s.traces.ListTraces("", "")
	traceEv := make([]evals.TraceEvidence, 0, len(traceList))
	for _, wf := range traceList {
		spanNames := make([]string, 0, len(wf.Spans))
		genSys := ""
		for _, sp := range wf.Spans {
			spanNames = append(spanNames, sp.Name)
			if genSys == "" {
				if v, ok := sp.Attributes["gen_ai.system"].(string); ok {
					genSys = v
				}
			}
			if b, err := json.Marshal(sp.Attributes); err == nil {
				inspectionBlob.Write(b)
				inspectionBlob.WriteByte('\n')
			}
		}
		// Always inspect span attributes for credential leak detection, then exclude
		// standalone unassociated spans (such as direct external /mcp/* security
		// verification probes or /api/evals/run calls with empty session and incident
		// IDs) from TrajectorySnapshot.Traces so autoraters evaluate only Actor turns.
		if strings.TrimSpace(wf.SessionID) == "" && strings.TrimSpace(wf.IncidentID) == "" {
			continue
		}
		traceEv = append(traceEv, evals.TraceEvidence{
			TraceID:      wf.TraceID,
			IncidentID:   wf.IncidentID,
			SessionID:    wf.SessionID,
			Status:       wf.Status,
			SpanNames:    spanNames,
			GenAISystem:  genSys,
			AgentURN:     wf.AgentURN,
			InputTokens:  wf.InputTokens,
			OutputTokens: wf.OutputTokens,
		})
	}

	mergedPRs := 0
	dualPRs := 0
	for _, pr := range s.fetchPullsFromOpsHub(ctx) {
		if strings.EqualFold(pr.Status, "MERGED") {
			mergedPRs++
		}
		if pr.AgenticDelegation && strings.TrimSpace(pr.OperatorSub) != "" && strings.HasPrefix(pr.AgentIdentity, "spiffe://") {
			dualPRs++
		}
	}

	inc402Resolved := false
	for _, inc := range s.fetchIncidentsFromOpsHub(ctx) {
		if strings.EqualFold(inc.ID, "INC-402") && strings.EqualFold(inc.Status, "RESOLVED") {
			inc402Resolved = true
			break
		}
	}

	s.mu.Lock()
	eventsCopy := append([]PolicyEvent(nil), s.policyEvents...)
	s.mu.Unlock()

	remediationSuspended := s.remediationSuspended.Load()
	for _, tr := range traceEv {
		if slices.Contains(tr.SpanNames, "substrate.suspend_actor") {
			remediationSuspended = true
			break
		}
	}
	policySummaries := make([]string, 0, len(eventsCopy))
	for _, pe := range eventsCopy {
		if pe.Verdict == "SUSPENDED" && (pe.Boundary == "Remediation Gate" || strings.Contains(pe.Action, "inc-402")) {
			remediationSuspended = true
		}
		line := pe.Boundary + ": " + pe.Action + " [" + pe.Verdict + "] " + pe.Detail
		policySummaries = append(policySummaries, line)
		inspectionBlob.WriteString(line + "\n")
	}

	extraSecrets := []string{s.cfg.OAuthClientSecret, s.cfg.GitHubOAuthClientSecret, s.cfg.AdminToken}
	leakedHits := telemetry.CountTokenLeaks(inspectionBlob.String(), extraSecrets)

	armorSnap := s.modelArmor.Status()
	oauthSnap := s.authManager.Status(default3LOOperator)

	return evals.TrajectorySnapshot{
		ProjectID:            firstNonEmpty(s.cfg.ProjectID, "local-dev"),
		Atespace:             s.cfg.Atespace,
		DefaultTemplate:      s.cfg.DefaultTemplate,
		Sessions:             sessions,
		MemoryFacts:          memFacts,
		SkillName:            skillName,
		SkillDefaultRevision: skillDefault,
		SkillRevisions:       skillRevs,
		SkillSHA256:          skillSHA,
		Traces:               traceEv,
		ScrubbedSpanAttrs:    s.traces.ScrubbedCount(),
		LeakedTokenHits:      leakedHits,
		ModelArmorEnabled:    armorSnap.Enabled,
		ModelArmorBlocked:    armorSnap.BlockedCount,
		MCPAllowedCount:      s.mcpAllowedCount.Load(),
		MCPDeniedCount:       s.mcpDeniedCount.Load(),
		OAuth3LOGranted:      oauthSnap.Granted,
		MergedRemediationPRs: mergedPRs,
		DualAttributedPRs:    dualPRs,
		RemediationSuspended: remediationSuspended,
		INC402Resolved:       inc402Resolved,
		PolicySummaries:      policySummaries,
	}
}
