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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/httptransport"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/telemetry"
)

const maxGenAIPayloadBytes = 4 << 20 // 4 MiB request cap for /genai proxy.

// genaiProxy forwards keyless /genai requests from Incident Actors to Gemini on
// Gemini platform via Workload Identity, or serves a deterministic
// multi-step ADK tool-calling responder when MockLLM is enabled.
type genaiProxy struct {
	server        *Server
	mockLLM       bool
	projectID     string
	modelLocation string
	defaultModel  string
	upstreamBase  string
	authedClient  *http.Client
	recordPolicy  func(PolicyEvent)
}

func newGenAIProxy(ctx context.Context, srv *Server, cfg Config, record func(PolicyEvent)) (*genaiProxy, error) {
	loc := firstNonEmpty(cfg.ModelLocation, "global")
	modelName := firstNonEmpty(cfg.GeminiModel, "gemini-3.8-flash")
	p := &genaiProxy{
		server:        srv,
		mockLLM:       cfg.MockLLM,
		projectID:     strings.TrimSpace(cfg.ProjectID),
		modelLocation: loc,
		defaultModel:  modelName,
		upstreamBase:  strings.TrimRight(strings.TrimSpace(cfg.GenAIUpstreamURL), "/"),
		recordPolicy:  record,
	}
	if p.mockLLM {
		return p, nil
	}
	if p.upstreamBase != "" {
		p.authedClient = &http.Client{Timeout: 90 * time.Second}
		return p, nil
	}
	if p.projectID == "" {
		return nil, errors.New("GOOGLE_CLOUD_PROJECT is required when MOCK_LLM=false")
	}
	creds, err := credentials.DetectDefault(&credentials.DetectOptions{
		Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
	})
	if err != nil {
		return nil, fmt.Errorf("detect Workload Identity credentials for Gemini platform: %w", err)
	}
	hc, err := httptransport.NewClient(&httptransport.Options{
		Credentials: creds,
	})
	if err != nil {
		return nil, fmt.Errorf("build authenticated HTTP client for Gemini platform: %w", err)
	}
	hc.Timeout = 90 * time.Second
	p.authedClient = hc
	_ = ctx
	return p, nil
}

func (p *genaiProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGenAIPayloadBytes))
	if err != nil {
		writeBrokerJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "genai payload exceeds limit"})
		return
	}

	modelName, method := parseGenAIPath(r.URL.Path, p.defaultModel)
	if method != "generateContent" && method != "streamGenerateContent" {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported genai method"})
		return
	}

	reqCtx, _, _ := telemetry.ExtractHTTP(r.Context(), r.Header)
	var finishSpan func(status, msg string, extra map[string]any)
	if p.server != nil {
		reqCtx, _, finishSpan = p.server.startBrokerSpan(
			reqCtx,
			"genai.generate_content",
			"CLIENT",
			"Gemini platform",
			map[string]any{
				"gen_ai.operation.name": "generate_content",
				"gen_ai.request.model":  modelName,
			},
		)
	}

	if p.mockLLM {
		respObj, detail := buildMockToolCallingResponse(rawBody)
		inTok, outTok := extractUsageFromMap(respObj)
		if p.server != nil && p.server.metrics != nil {
			p.server.metrics.ObserveGenAI(modelName, "ok", inTok, outTok)
		}
		if finishSpan != nil {
			finishSpan("OK", "", map[string]any{
				"gen_ai.usage.input_tokens":  inTok,
				"gen_ai.usage.output_tokens": outTok,
			})
		}
		if p.recordPolicy != nil {
			p.recordPolicy(PolicyEvent{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Boundary:  "Model Garden (/genai)",
				Action:    method + " (" + modelName + ")",
				Verdict:   "ALLOW",
				Detail:    "Keyless actor request handled by deterministic MOCK_LLM responder: " + detail,
			})
		}
		if method == "streamGenerateContent" && strings.EqualFold(r.URL.Query().Get("alt"), "sse") {
			b, _ := json.Marshal(respObj)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", string(b))
			return
		}
		writeBrokerJSON(w, http.StatusOK, respObj)
		return
	}

	targetURL := p.buildEnterpriseEndpoint(modelName, method, r.URL.RawQuery)
	upReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, targetURL, bytes.NewReader(rawBody))
	if err != nil {
		if finishSpan != nil {
			finishSpan("ERROR", "construct upstream request failed", nil)
		}
		writeBrokerJSON(w, http.StatusInternalServerError, map[string]string{"error": "construct upstream request failed"})
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	if ua := strings.TrimSpace(r.Header.Get("User-Agent")); ua != "" {
		upReq.Header.Set("User-Agent", ua)
	}
	if apiClient := strings.TrimSpace(r.Header.Get("X-Goog-Api-Client")); apiClient != "" {
		upReq.Header.Set("X-Goog-Api-Client", apiClient)
	}

	resp, err := p.authedClient.Do(upReq)
	if err != nil {
		if p.server != nil && p.server.metrics != nil {
			p.server.metrics.ObserveGenAI(modelName, "error", 0, 0)
		}
		if finishSpan != nil {
			finishSpan("ERROR", err.Error(), nil)
		}
		if p.recordPolicy != nil {
			p.recordPolicy(PolicyEvent{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Boundary:  "Model Garden (/genai)",
				Action:    method + " (" + modelName + ")",
				Verdict:   "ERROR",
				Detail:    "Upstream Gemini platform call failed: " + err.Error(),
			})
		}
		writeBrokerJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream Gemini platform call failed: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	inTok, outTok := extractUsageFromBytes(respBytes)
	spanStatus := "OK"
	metricStatus := "ok"
	verdict := "ALLOW"
	if resp.StatusCode >= 400 {
		verdict = fmt.Sprintf("HTTP_%d", resp.StatusCode)
		spanStatus = "ERROR"
		metricStatus = "error"
	}
	if p.server != nil && p.server.metrics != nil {
		p.server.metrics.ObserveGenAI(modelName, metricStatus, inTok, outTok)
	}
	if finishSpan != nil {
		finishSpan(spanStatus, "", map[string]any{
			"gen_ai.usage.input_tokens":  inTok,
			"gen_ai.usage.output_tokens": outTok,
		})
	}
	if p.recordPolicy != nil {
		p.recordPolicy(PolicyEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Boundary:  "Model Garden (/genai)",
			Action:    method + " (" + modelName + ")",
			Verdict:   verdict,
			Detail:    "Attached Workload Identity outside sandbox -> Gemini platform (" + p.modelLocation + ")",
		})
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBytes)
}

func extractUsageFromMap(respObj map[string]any) (int64, int64) {
	um, ok := respObj["usageMetadata"].(map[string]any)
	if !ok {
		return 0, 0
	}
	toInt := func(v any) int64 {
		switch n := v.(type) {
		case int:
			return int64(n)
		case int64:
			return n
		case float64:
			return int64(n)
		default:
			return 0
		}
	}
	return toInt(um["promptTokenCount"]), toInt(um["candidatesTokenCount"])
}

func extractUsageFromBytes(raw []byte) (int64, int64) {
	if len(raw) == 0 {
		return 0, 0
	}
	type usageDoc struct {
		UsageMetadata struct {
			PromptTokenCount        int64 `json:"promptTokenCount"`
			ToolUsePromptTokenCount int64 `json:"toolUsePromptTokenCount"`
			CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
			ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}
	var single usageDoc
	if json.Unmarshal(raw, &single) == nil && (single.UsageMetadata.PromptTokenCount > 0 || single.UsageMetadata.CandidatesTokenCount > 0) {
		return single.UsageMetadata.PromptTokenCount + single.UsageMetadata.ToolUsePromptTokenCount,
			single.UsageMetadata.CandidatesTokenCount + single.UsageMetadata.ThoughtsTokenCount
	}
	var inTok, outTok int64
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		var chunk usageDoc
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))), &chunk) == nil {
			if chunk.UsageMetadata.PromptTokenCount > 0 {
				inTok = chunk.UsageMetadata.PromptTokenCount + chunk.UsageMetadata.ToolUsePromptTokenCount
			}
			if chunk.UsageMetadata.CandidatesTokenCount+chunk.UsageMetadata.ThoughtsTokenCount > 0 {
				outTok = chunk.UsageMetadata.CandidatesTokenCount + chunk.UsageMetadata.ThoughtsTokenCount
			}
		}
	}
	return inTok, outTok
}

func parseGenAIPath(urlPath, fallbackModel string) (modelName, method string) {
	trimmed := strings.TrimPrefix(urlPath, "/genai/")
	colonIdx := strings.LastIndexByte(trimmed, ':')
	if colonIdx < 0 {
		return fallbackModel, ""
	}
	method = trimmed[colonIdx+1:]
	prefix := trimmed[:colonIdx]
	if i := strings.LastIndex(prefix, "/models/"); i >= 0 {
		modelName = prefix[i+len("/models/"):]
	} else if strings.HasPrefix(prefix, "models/") {
		modelName = strings.TrimPrefix(prefix, "models/")
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = fallbackModel
	}
	return modelName, method
}

func (p *genaiProxy) buildEnterpriseEndpoint(modelName, method, rawQuery string) string {
	base := p.upstreamBase
	if base == "" {
		switch p.modelLocation {
		case "global":
			base = "https://aiplatform.googleapis.com"
		case "us", "eu":
			base = fmt.Sprintf("https://aiplatform.%s.rep.googleapis.com", p.modelLocation)
		default:
			base = fmt.Sprintf("https://%s-aiplatform.googleapis.com", p.modelLocation)
		}
	}
	u := fmt.Sprintf(
		"%s/v1beta1/projects/%s/locations/%s/publishers/google/models/%s:%s",
		base, p.projectID, p.modelLocation, modelName, method,
	)
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	return u
}

// buildMockToolCallingResponse inspects ADK Go v2's genai GenerateContent request
// and drives a deterministic multi-step tool-calling loop over memory and workspace tools:
// optional load_memory -> clone_repo -> read_workspace_file ->
// run_workspace_command (ledger-verify) -> optional stage_git_patch -> grounded diagnosis.
func buildMockToolCallingResponse(raw []byte) (map[string]any, string) {
	var payload struct {
		SystemInstruction *struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"systemInstruction"`
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text         string `json:"text"`
				FunctionCall *struct {
					ID   string         `json:"id"`
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				} `json:"functionCall"`
				FunctionResponse *struct {
					ID       string         `json:"id"`
					Name     string         `json:"name"`
					Response map[string]any `json:"response"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
		Tools []struct {
			FunctionDeclarations []struct {
				Name string `json:"name"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(raw, &payload)

	declared := map[string]bool{}
	for _, t := range payload.Tools {
		for _, fd := range t.FunctionDeclarations {
			declared[fd.Name] = true
		}
	}

	// Locate the start of the active operator turn so multi-turn sessions persisted
	// in the Session Ledger evaluate tool progress per turn.
	turnStartIdx := 0
	var lastUserPrompt string
	for i, c := range payload.Contents {
		if c.Role != "user" {
			continue
		}
		hasText := false
		hasFuncResp := false
		var promptText string
		for _, pt := range c.Parts {
			if pt.FunctionResponse != nil {
				hasFuncResp = true
			}
			if pt.Text != "" {
				hasText = true
				promptText = pt.Text
			}
		}
		if hasText && !hasFuncResp {
			turnStartIdx = i
			lastUserPrompt = promptText
		}
	}

	everClonedInSession := false
	for _, c := range payload.Contents {
		for _, pt := range c.Parts {
			if pt.FunctionResponse != nil && pt.FunctionResponse.Name == "clone_repo" {
				if cloned, _ := pt.FunctionResponse.Response["cloned"].(bool); cloned {
					everClonedInSession = true
				}
			}
		}
	}

	completed := map[string]int{}
	var storeGoContent string
	var recalledSnippets []string
	var skillScriptOutput string
	var lastApplyPRResp map[string]any
	skillScriptMissingDir := false
	verifyRan := false
	verifyAfterPatchRan := false

	if payload.SystemInstruction != nil {
		for _, pt := range payload.SystemInstruction.Parts {
			if idx := strings.Index(pt.Text, "<PAST_CONVERSATIONS>"); idx >= 0 {
				endIdx := strings.Index(pt.Text, "</PAST_CONVERSATIONS>")
				if endIdx > idx {
					body := strings.TrimSpace(pt.Text[idx+len("<PAST_CONVERSATIONS>") : endIdx])
					if body != "" {
						recalledSnippets = append(recalledSnippets, body)
					}
				}
			}
		}
	}

	for _, c := range payload.Contents[turnStartIdx:] {
		for _, pt := range c.Parts {
			if pt.FunctionResponse == nil {
				continue
			}
			completed[pt.FunctionResponse.Name]++
			switch pt.FunctionResponse.Name {
			case "clone_repo":
				skillScriptMissingDir = false
			case "read_workspace_file":
				if pathVal, _ := pt.FunctionResponse.Response["path"].(string); strings.HasSuffix(pathVal, "pool/store.go") {
					if bodyVal, _ := pt.FunctionResponse.Response["content"].(string); bodyVal != "" {
						storeGoContent = bodyVal
					}
				}
			case "run_workspace_command":
				cmdVal, _ := pt.FunctionResponse.Response["command"].(string)
				outVal, _ := pt.FunctionResponse.Response["output"].(string)
				if cmdVal == "sh" {
					if strings.Contains(outVal, "does not exist in /workspace") {
						skillScriptMissingDir = true
					} else if strings.TrimSpace(outVal) != "" {
						skillScriptOutput = strings.TrimSpace(outVal)
						skillScriptMissingDir = false
					}
				} else if cmdVal == "ledger-verify" {
					verifyRan = true
					if completed["stage_git_patch"] > 0 {
						verifyAfterPatchRan = true
					}
				}
			case "load_memory":
				recalledSnippets = append(recalledSnippets, extractRecalledMemoryTexts(pt.FunctionResponse.Response)...)
			case "apply_remediation_pr":
				lastApplyPRResp = pt.FunctionResponse.Response
			}
		}
	}

	// If apply_remediation_pr already completed (approved or rejected after a
	// Remediation Gate wake), synthesize the post-gate summary directly.
	if lastApplyPRResp != nil {
		errText, _ := lastApplyPRResp["error"].(string)
		if strings.Contains(strings.ToLower(errText), "confirmation rejected") {
			return mockCandidateParts("remediation_rejected_summary", []map[string]any{
				{
					"text": "Operator **rejected** the Remediation Gate for `INC-402` (`confirmed=false`).\n\n" +
						"- Verified branch `fix/inc-402-pool-leak` remains staged in `/workspace/ledger-api` without merging into `main`.\n" +
						"- `ops-hub` pull requests and `ledger-api` runtime configuration were left unchanged.",
				},
			}), "emit post-gate rejection summary"
		}
		if merged, _ := lastApplyPRResp["merged"].(bool); merged {
			prID, _ := lastApplyPRResp["pr_id"].(string)
			if prID == "" {
				prID = "PR-1"
			}
			branchName, _ := lastApplyPRResp["branch"].(string)
			if branchName == "" {
				branchName = "fix/inc-402-pool-leak"
			}
			commitSHA, _ := lastApplyPRResp["commit_sha"].(string)
			reply := fmt.Sprintf(
				"Remediation Gate **approved** — Incident Actor resumed from Cloud Storage (`< 400ms`) and completed `apply_remediation_pr`:\n\n"+
					"- **Merged pull request**: `%s` (`%s` -> `main`, commit `%s`)\n"+
					"- **Applied configuration**: `max_open_conns: 32`, `idle_timeout_seconds: 30`, and `defer rows.Close()` cursor-leak fix in `pool/store.go`\n"+
					"- **Target Service verification**: `ledger-api` reloaded cleanly and `/healthz` returned `200 OK` (`INC-402` marked `RESOLVED`)",
				prID, branchName, commitSHA,
			)
			return mockCandidateParts("remediation_approved_summary", []map[string]any{
				{"text": reply},
			}), "emit post-gate approval summary (" + prID + ")"
		}
	}

	// Extract only the operator's request tail when [Incident ... context] was prepended.
	operatorPrompt := lastUserPrompt
	if idx := strings.Index(lastUserPrompt, "[Operator request]\n"); idx >= 0 {
		operatorPrompt = lastUserPrompt[idx+len("[Operator request]\n"):]
	}
	lowerOpPrompt := strings.ToLower(operatorPrompt)

	// Step 0: Query Consolidated Memory when ADK Go v2's load_memory tool is attached (sre-agent-v2+).
	if declared["load_memory"] && completed["load_memory"] == 0 {
		memQuery := "INC-402 ledger-api connection pool exhaustion MAX_OPEN_CONNS IDLE_TIMEOUT_SECONDS"
		if strings.Contains(strings.ToUpper(lastUserPrompt), "INC-403") {
			memQuery = "INC-403 ledger-api 503 pool saturation INC-402 MAX_OPEN_CONNS IDLE_TIMEOUT_SECONDS"
		}
		return mockCandidateParts("call_load_memory", []map[string]any{
			{
				"functionCall": map[string]any{
					"id":   "call_load_memory_1",
					"name": "load_memory",
					"args": map[string]any{"query": memQuery},
				},
			},
		}), "emit load_memory(" + memQuery + ")"
	}

	hasMCPTools := declared["query_service_error_logs"] || declared["get_deployment_health"]
	hasRemediationGateTools := declared["apply_remediation_pr"] && declared["push_git_branch"]

	wantsPatch := strings.Contains(lowerOpPrompt, "stage") ||
		strings.Contains(lowerOpPrompt, "patch") ||
		strings.Contains(lowerOpPrompt, "remediat") ||
		strings.Contains(lowerOpPrompt, "apply_remediation_pr")
	wantsApplyRemediation := hasRemediationGateTools && (strings.Contains(lowerOpPrompt, "remediat") ||
		strings.Contains(lowerOpPrompt, "apply_remediation_pr") ||
		strings.Contains(lowerOpPrompt, "merge") ||
		strings.Contains(lowerOpPrompt, "pull request"))
	explicitNoClone := strings.Contains(lowerOpPrompt, "without re-cloning") ||
		strings.Contains(lowerOpPrompt, "without cloning")
	memoryOnlyAsk := strings.Contains(lowerOpPrompt, "memory") &&
		!strings.Contains(lowerOpPrompt, "skill") &&
		!strings.Contains(lowerOpPrompt, "diagnos") &&
		!strings.Contains(lowerOpPrompt, "runbook") &&
		!wantsPatch
	wantsSkillRun := declared["load_skill"] && !explicitNoClone && !memoryOnlyAsk &&
		(!hasRemediationGateTools || strings.Contains(lowerOpPrompt, "skill") || !wantsPatch)
	wantsWorkspaceInspect := !explicitNoClone && (wantsPatch ||
		strings.Contains(lowerOpPrompt, "clone") ||
		strings.Contains(lowerOpPrompt, "pool.yaml") ||
		strings.Contains(lowerOpPrompt, "store.go") ||
		strings.Contains(lowerOpPrompt, "ledger-verify") ||
		strings.Contains(lowerOpPrompt, "/workspace"))

	if len(recalledSnippets) > 0 && !wantsWorkspaceInspect && !wantsSkillRun {
		joinedRecall := strings.Join(recalledSnippets, "\n")
		incTarget := "INC-402"
		if strings.Contains(strings.ToUpper(lastUserPrompt), "INC-403") {
			incTarget = "INC-403"
		}
		reply := fmt.Sprintf(
			"Recalled verified `ledger-api` findings from **Consolidated Memory** for %s (zero redundant git clone required):\n\n"+
				"- **Prior incident correlation (`INC-402`)**: Connection pool starvation in `config/pool.yaml` (`MAX_OPEN_CONNS=2`, `IDLE_TIMEOUT_SECONDS=0`) combined with an unclosed cursor leak in `pool/store.go` (`ReconcileTransfer` calling `rows.ValidateChecksum()` before `rows.Close()`).\n"+
				"- **Validated remediation runbook**: Add `defer rows.Close()` immediately after `AcquireRows` in `ReconcileTransfer` and tune `config/pool.yaml` to `max_open_conns: 32` and `idle_timeout_seconds: 30`.\n\n"+
				"Memory Bank excerpt:\n%s",
			incTarget,
			joinedRecall,
		)
		return mockCandidateParts("memory_recall_summary", []map[string]any{
			{"text": reply},
		}), "emit grounded diagnosis from Consolidated Memory recall"
	}

	// Module 04+ Agent Registry MCP observability step: query Cloud Logging & GKE health.
	if hasMCPTools && !explicitNoClone && !memoryOnlyAsk {
		var mcpCalls []map[string]any
		if declared["query_service_error_logs"] && completed["query_service_error_logs"] == 0 {
			mcpCalls = append(mcpCalls, map[string]any{
				"functionCall": map[string]any{
					"id":   "call_mcp_logs_1",
					"name": "query_service_error_logs",
					"args": map[string]any{
						"service":   "ledger-api",
						"namespace": "prod-sim",
						"severity":  "ERROR",
						"limit":     5,
					},
				},
			})
		}
		if declared["get_deployment_health"] && completed["get_deployment_health"] == 0 {
			mcpCalls = append(mcpCalls, map[string]any{
				"functionCall": map[string]any{
					"id":   "call_mcp_health_1",
					"name": "get_deployment_health",
					"args": map[string]any{
						"namespace":  "prod-sim",
						"deployment": "ledger-api",
					},
				},
			})
		}
		if len(mcpCalls) > 0 {
			return mockCandidateParts("call_mcp_observability", mcpCalls), "emit Agent Registry MCP observability calls"
		}
	}

	// Module 03+ progressive SkillToolset loop: load_skill(ledger-pool-triage) ->
	// clone_repo (if uncloned) -> run_workspace_command(sh .skills/ledger-pool-triage/scripts/diagnose_pool.sh).
	if wantsSkillRun {
		if completed["load_skill"] == 0 {
			return mockCandidateParts("call_load_skill", []map[string]any{
				{
					"functionCall": map[string]any{
						"id":   "call_load_skill_1",
						"name": "load_skill",
						"args": map[string]any{"name": "ledger-pool-triage"},
					},
				},
			}), "emit load_skill(ledger-pool-triage)"
		}
		if (!everClonedInSession || skillScriptMissingDir) && declared["clone_repo"] && completed["clone_repo"] == 0 {
			return mockCandidateParts("call_clone_for_skill", []map[string]any{
				{
					"functionCall": map[string]any{
						"id":   "call_clone_1",
						"name": "clone_repo",
						"args": map[string]any{"repo": "ledger-api"},
					},
				},
			}), "emit clone_repo(ledger-api)"
		}
		if skillScriptOutput == "" && declared["run_workspace_command"] {
			return mockCandidateParts("call_skill_script", []map[string]any{
				{
					"functionCall": map[string]any{
						"id":   "call_skill_diag_1",
						"name": "run_workspace_command",
						"args": map[string]any{
							"command": "sh",
							"args":    []string{".skills/ledger-pool-triage/scripts/diagnose_pool.sh"},
							"dir":     "ledger-api",
						},
					},
				},
			}), "emit run_workspace_command(sh .skills/ledger-pool-triage/scripts/diagnose_pool.sh)"
		}
		if !wantsPatch && !strings.Contains(lowerOpPrompt, "ledger-verify") {
			reply := buildSkillDiagnosticSummary(skillScriptOutput, recalledSnippets)
			return mockCandidateParts("skill_summary", []map[string]any{
				{"text": reply},
			}), "emit grounded Procedural Skill diagnostic summary"
		}
	}

	// Step 1: Clone ledger-api into /workspace/ledger-api.
	if declared["clone_repo"] && completed["clone_repo"] == 0 && (!everClonedInSession || !wantsSkillRun) {
		return mockCandidateParts("call_clone_repo", []map[string]any{
			{
				"functionCall": map[string]any{
					"id":   "call_clone_1",
					"name": "clone_repo",
					"args": map[string]any{"repo": "ledger-api"},
				},
			},
		}), "emit clone_repo(ledger-api)"
	}

	// Step 2: Read config/pool.yaml and pool/store.go inside /workspace/ledger-api.
	if declared["read_workspace_file"] && completed["read_workspace_file"] == 0 {
		return mockCandidateParts("call_read_files", []map[string]any{
			{
				"functionCall": map[string]any{
					"id":   "call_read_pool_yaml",
					"name": "read_workspace_file",
					"args": map[string]any{"path": "ledger-api/config/pool.yaml"},
				},
			},
			{
				"functionCall": map[string]any{
					"id":   "call_read_store_go",
					"name": "read_workspace_file",
					"args": map[string]any{"path": "ledger-api/pool/store.go"},
				},
			},
		}), "emit read_workspace_file(config/pool.yaml, pool/store.go)"
	}

	patchBranch := "fix/inc-402-pool"
	if hasRemediationGateTools {
		patchBranch = "fix/inc-402-pool-leak"
	}

	// Step 3a (Modules 01-03 or diagnostic-only turns): Run ledger-verify before patching.
	if declared["run_workspace_command"] && !verifyRan && (!hasRemediationGateTools || !wantsPatch) {
		return mockCandidateParts("call_verify", []map[string]any{
			{
				"functionCall": map[string]any{
					"id":   "call_ledger_verify",
					"name": "run_workspace_command",
					"args": map[string]any{
						"command": "ledger-verify",
						"dir":     "ledger-api",
					},
				},
			},
		}), "emit run_workspace_command(ledger-verify)"
	}

	// Step 4: Stage git patch when operator prompt asks to stage/patch/remediate.
	if wantsPatch && declared["stage_git_patch"] && completed["stage_git_patch"] == 0 && storeGoContent != "" {
		patchedStore := storeGoContent
		if !strings.Contains(patchedStore, "defer rows.Close()") {
			patchedStore = strings.Replace(
				patchedStore,
				"if err := rows.ValidateChecksum(); err != nil {",
				"defer rows.Close()\n\tif err := rows.ValidateChecksum(); err != nil {",
				1,
			)
		}
		return mockCandidateParts("call_stage_patch", []map[string]any{
			{
				"functionCall": map[string]any{
					"id":   "call_stage_git_patch",
					"name": "stage_git_patch",
					"args": map[string]any{
						"branch":         patchBranch,
						"commit_message": "Fix connection pool sizing and close rows cursor on checksum error",
						"files": map[string]string{
							"config/pool.yaml": "max_open_conns: 32\nidle_timeout_seconds: 30\nacquire_timeout_ms: 250\n",
							"pool/store.go":    patchedStore,
						},
					},
				},
			},
		}), "emit stage_git_patch(" + patchBranch + ")"
	}

	// Step 4b (Module 04+): Verify staged patch with ledger-verify (exit 0), push branch, and invoke apply_remediation_pr.
	if hasRemediationGateTools && wantsPatch {
		if declared["run_workspace_command"] && !verifyAfterPatchRan {
			return mockCandidateParts("call_verify_after_patch", []map[string]any{
				{
					"functionCall": map[string]any{
						"id":   "call_ledger_verify_post",
						"name": "run_workspace_command",
						"args": map[string]any{
							"command": "ledger-verify",
							"dir":     "ledger-api",
						},
					},
				},
			}), "emit run_workspace_command(ledger-verify) after stage_git_patch"
		}
		if declared["push_git_branch"] && completed["push_git_branch"] == 0 {
			return mockCandidateParts("call_push_branch", []map[string]any{
				{
					"functionCall": map[string]any{
						"id":   "call_push_git_branch_1",
						"name": "push_git_branch",
						"args": map[string]any{"branch": patchBranch},
					},
				},
			}), "emit push_git_branch(" + patchBranch + ")"
		}
		if wantsApplyRemediation && declared["apply_remediation_pr"] && completed["apply_remediation_pr"] == 0 {
			return mockCandidateParts("call_apply_remediation_pr", []map[string]any{
				{
					"functionCall": map[string]any{
						"id":   "call_apply_remediation_pr_1",
						"name": "apply_remediation_pr",
						"args": map[string]any{
							"incident_id": "INC-402",
							"branch":      patchBranch,
							"title":       "Fix ledger-api connection pool sizing and close rows cursor on checksum error",
						},
					},
				},
			}), "emit apply_remediation_pr(" + patchBranch + ") [Remediation Gate]"
		}
	}

	// Final synthesis grounded in the inspected /workspace artifacts and any recalled memories.
	summary := "Root cause verified in `/workspace/ledger-api`:\n\n" +
		"1. **Undersized pool with disabled idle reaper (`config/pool.yaml`)**: `max_open_conns: 2` (`MAX_OPEN_CONNS=2`) and `idle_timeout_seconds: 0` (`IDLE_TIMEOUT_SECONDS=0`) leave zero headroom once two reconciliation cursors stall.\n" +
		"2. **Unclosed cursor leak on checksum errors (`pool/store.go`)**: `ReconcileTransfer` calls `rows.ValidateChecksum()` before `rows.Close()`. Any `bad-*` transfer batch returns `ErrInvalidChecksum` while holding its `s.slots` semaphore token forever, driving `GET /healthz` and `POST /api/transfer` into HTTP `503 pool_exhausted`.\n" +
		"3. **Recommended remediation**: Add `defer rows.Close()` immediately after `AcquireRows` in `ReconcileTransfer` and raise `max_open_conns` to `32` with `idle_timeout_seconds: 30` in `config/pool.yaml`."
	if len(recalledSnippets) > 0 {
		summary += "\n\nCorrelated with Consolidated Memory:\n" + strings.Join(recalledSnippets, "\n")
	}
	if completed["stage_git_patch"] > 0 {
		summary += "\n\nStaged and verified commit on branch `" + patchBranch + "` inside `/workspace/ledger-api` (`ledger-verify` passed)."
	}
	if skillScriptOutput != "" {
		summary += "\n\nProcedural Skill diagnostic output:\n```\n" + skillScriptOutput + "\n```"
	}
	return mockCandidateParts("final_summary", []map[string]any{
		{"text": summary},
	}), "emit grounded diagnosis summary"
}

func buildSkillDiagnosticSummary(scriptOutput string, recalled []string) string {
	var b strings.Builder
	b.WriteString("Executed SHA-256-verified Procedural Skill `ledger-pool-triage` inside `/workspace/ledger-api`:\n\n")
	if strings.Contains(scriptOutput, "CURSOR_LEAK_DETECTED=true") {
		b.WriteString("- **Active skill revision**: `SKILL_REVISION=v2` (pool configuration + Go cursor-leak analysis).\n")
		b.WriteString("- **Pool sizing (`config/pool.yaml`)**: Undersized pool (`MAX_OPEN_CONNS=2`, `IDLE_TIMEOUT_SECONDS=0`) flagged as `SATURATION_RISK`.\n")
		b.WriteString("- **Go source leak (`pool/store.go:ReconcileTransfer`)**: `CURSOR_LEAK_DETECTED=true` — `rows.ValidateChecksum()` returns on checksum errors before `rows.Close()` releases the connection slot.\n\n")
	} else {
		b.WriteString("- **Active skill revision**: Baseline pool configuration diagnostic executed from `/workspace/.skills/ledger-pool-triage/scripts/diagnose_pool.sh`.\n")
		b.WriteString("- **Pool sizing (`config/pool.yaml`)**: Undersized pool (`MAX_OPEN_CONNS=2`, `IDLE_TIMEOUT_SECONDS=0`) flagged as `SATURATION_RISK`.\n\n")
	}
	b.WriteString("Diagnostic script stdout:\n```\n")
	b.WriteString(scriptOutput)
	b.WriteString("\n```")
	if len(recalled) > 0 {
		b.WriteString("\n\nCorrelated with Consolidated Memory:\n")
		b.WriteString(strings.Join(recalled, "\n"))
	}
	return b.String()
}

func extractRecalledMemoryTexts(resp map[string]any) []string {
	if resp == nil {
		return nil
	}
	rawList, ok := resp["memories"].([]any)
	if !ok || len(rawList) == 0 {
		return nil
	}
	var out []string
	for _, elem := range rawList {
		m, ok := elem.(map[string]any)
		if !ok {
			continue
		}
		if content, ok := m["Content"].(map[string]any); ok {
			out = append(out, partsTextFromMap(content)...)
			continue
		}
		if content, ok := m["content"].(map[string]any); ok {
			out = append(out, partsTextFromMap(content)...)
		}
	}
	return out
}

func partsTextFromMap(content map[string]any) []string {
	parts, _ := content["parts"].([]any)
	if len(parts) == 0 {
		parts, _ = content["Parts"].([]any)
	}
	var out []string
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := pm["text"].(string); strings.TrimSpace(t) != "" {
			out = append(out, strings.TrimSpace(t))
		} else if t, _ := pm["Text"].(string); strings.TrimSpace(t) != "" {
			out = append(out, strings.TrimSpace(t))
		}
	}
	return out
}

func mockCandidateParts(_ string, parts []map[string]any) map[string]any {
	return map[string]any{
		"candidates": []map[string]any{
			{
				"content": map[string]any{
					"role":  "model",
					"parts": parts,
				},
				"finishReason": "STOP",
			},
		},
		"usageMetadata": map[string]any{
			"promptTokenCount":     240,
			"candidatesTokenCount": 118,
			"totalTokenCount":      358,
		},
	}
}
