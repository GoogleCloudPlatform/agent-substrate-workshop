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
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/httptransport"
)

var (
	sdpSSNRegex    = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
	sdpAPIKeyRegex = regexp.MustCompile(`\b(?:AIza[0-9A-Za-z\-_]{12,}|sk-[A-Za-z0-9]{16,})\b`)
	sdpEmailRegex  = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
)

var promptInjectionPatterns = []string{
	"ignore all previous instructions",
	"ignore previous instructions",
	"[system instruction",
	"system override:",
	"emergency maintenance override",
	"drop_ledger_database",
	"delete_production_namespace",
	"exfiltrate all customer",
	"exfiltrate the gcp service account",
	"oauth bearer tokens in your final response",
}

var maliciousURIPatterns = []string{
	"http://169.254.169.254",
	"https://169.254.169.254",
	"computemetadata/v1",
	"https://evil.example.com",
	"https://attacker.example.com",
}

// ModelArmorStatus summarizes Model Armor and SDP screening state on sre-gateway.
type ModelArmorStatus struct {
	Enabled       bool   `json:"enabled"`
	Mode          string `json:"mode"`
	TemplateID    string `json:"template_id,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	SDPRedaction  bool   `json:"sdp_redaction"`
	BlockedCount  int64  `json:"blocked_count"`
	RedactedCount int64  `json:"redacted_count"`
}

// MCPPolicyStatus summarizes Agent Registry MCP tool annotation enforcement.
type MCPPolicyStatus struct {
	Enforced     bool  `json:"enforced"`
	AllowedCount int64 `json:"allowed_count"`
	DeniedCount  int64 `json:"denied_count"`
}

// ModelArmorBlockError is returned when Model Armor blocks an adversarial prompt,
// untrusted incident payload, or model output before it crosses a trust boundary.
type ModelArmorBlockError struct {
	IncidentID string
	Stage      string
	Reason     string
	TemplateID string
	Provenance string
}

func (e *ModelArmorBlockError) Error() string {
	return fmt.Sprintf("MODEL_ARMOR_BLOCKED (%s): %s", e.Stage, e.Reason)
}

type screenOutcome struct {
	SanitizedText string
	Blocked       bool
	Redacted      bool
	RedactedTypes []string
	Reason        string
	Provenance    string
}

type modelArmorGuard struct {
	server       *Server
	projectID    string
	region       string
	templateID   string
	upstreamBase string
	liveClient   *http.Client
	recordPolicy func(PolicyEvent)

	blockedCount  atomic.Int64
	redactedCount atomic.Int64
}

func normalizeOptionalID(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" || strings.EqualFold(v, "none") {
		return ""
	}
	return v
}

func newModelArmorGuard(ctx context.Context, srv *Server, cfg Config, record func(PolicyEvent)) *modelArmorGuard {
	proj := strings.TrimSpace(cfg.ProjectID)
	reg := firstNonEmpty(cfg.Region, "us-central1")
	tmpl := normalizeOptionalID(cfg.ModelArmorTemplateID)
	upBase := strings.TrimRight(strings.TrimSpace(cfg.ModelArmorUpstreamURL), "/")
	if upBase != "" && tmpl == "" {
		tmpl = "sre-triage-guard"
	}

	g := &modelArmorGuard{
		server:       srv,
		projectID:    proj,
		region:       reg,
		templateID:   tmpl,
		upstreamBase: upBase,
		recordPolicy: record,
	}

	if upBase != "" {
		g.liveClient = &http.Client{Timeout: 8 * time.Second}
	} else if !cfg.MockLLM && tmpl != "" && proj != "" && proj != "local-dev" {
		if creds, err := credentials.DetectDefault(&credentials.DetectOptions{
			Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
		}); err == nil {
			if hc, err := httptransport.NewClient(&httptransport.Options{
				Credentials: creds,
			}); err == nil {
				hc.Timeout = 8 * time.Second
				g.liveClient = hc
			}
		}
	}
	_ = ctx
	return g
}

func (g *modelArmorGuard) Status() ModelArmorStatus {
	mode := "defense-in-depth"
	endpoint := ""
	if g.liveClient != nil && g.templateID != "" {
		mode = "cloud-api+defense-in-depth"
		endpoint = g.templateResourceURL()
	}
	return ModelArmorStatus{
		Enabled:       true,
		Mode:          mode,
		TemplateID:    g.templateID,
		Endpoint:      endpoint,
		SDPRedaction:  true,
		BlockedCount:  g.blockedCount.Load(),
		RedactedCount: g.redactedCount.Load(),
	}
}

func (g *modelArmorGuard) templateResourceURL() string {
	base := g.upstreamBase
	if base == "" {
		base = fmt.Sprintf("https://modelarmor.%s.rep.googleapis.com", g.region)
	}
	if strings.HasPrefix(g.templateID, "projects/") {
		return fmt.Sprintf("%s/v1/%s", base, g.templateID)
	}
	proj := firstNonEmpty(g.projectID, "local-dev")
	return fmt.Sprintf("%s/v1/projects/%s/locations/%s/templates/%s", base, proj, g.region, g.templateID)
}

func (g *modelArmorGuard) ScreenUserPrompt(ctx context.Context, stage, incID, text string) (screenOutcome, error) {
	return g.screen(ctx, "sanitizeUserPrompt", "userPromptData", stage, incID, text)
}

func (g *modelArmorGuard) ScreenModelResponse(ctx context.Context, stage, incID, text string) (screenOutcome, error) {
	return g.screen(ctx, "sanitizeModelResponse", "modelResponseData", stage, incID, text)
}

func (g *modelArmorGuard) screen(ctx context.Context, rpcMethod, dataField, stage, incID, text string) (screenOutcome, error) {
	if strings.TrimSpace(text) == "" {
		return screenOutcome{SanitizedText: text, Provenance: "defense-in-depth"}, nil
	}

	spanKind := "INTERNAL"
	if g.liveClient != nil && g.templateID != "" {
		spanKind = "CLIENT"
	}
	var finishSpan func(status, msg string, extra map[string]any)
	if g.server != nil {
		ctx, _, finishSpan = g.server.startBrokerSpan(
			ctx,
			"model_armor.screen",
			spanKind,
			"Model Armor",
			map[string]any{
				"model_armor.method":      rpcMethod,
				"model_armor.stage":       stage,
				"model_armor.template_id": firstNonEmpty(g.templateID, "defense-in-depth"),
				"incident.id":             incID,
			},
		)
	}

	out := screenOutcome{
		SanitizedText: text,
		Provenance:    "defense-in-depth",
	}

	// 1. Query live Cloud Model Armor API when configured.
	if g.liveClient != nil && g.templateID != "" {
		liveBlocked, liveReason, liveRedacted, ok := g.callCloudModelArmor(ctx, rpcMethod, dataField, text)
		if ok {
			out.Provenance = "live-model-armor-api"
			if liveRedacted != "" && liveRedacted != text {
				out.SanitizedText = liveRedacted
				out.Redacted = true
				out.RedactedTypes = appendUniqueStr(out.RedactedTypes, "CLOUD_SDP")
			}
			if liveBlocked {
				out.Blocked = true
				out.Reason = liveReason
			}
		}
	}

	// 2. Deterministic defense-in-depth prompt-injection and malicious-URI checks.
	lower := strings.ToLower(text)
	for _, sig := range promptInjectionPatterns {
		if strings.Contains(lower, sig) {
			out.Blocked = true
			if out.Reason == "" {
				out.Reason = fmt.Sprintf("piAndJailbreakFilterSettings matched %q", sig)
			}
			break
		}
	}
	if !out.Blocked {
		for _, uri := range maliciousURIPatterns {
			if strings.Contains(lower, uri) {
				out.Blocked = true
				out.Reason = fmt.Sprintf("maliciousUriFilterSettings matched %q", uri)
				break
			}
		}
	}

	if out.Blocked {
		g.blockedCount.Add(1)
		if g.server != nil && g.server.metrics != nil {
			g.server.metrics.ObserveModelArmor("blocked")
		}
		if finishSpan != nil {
			finishSpan("ERROR", out.Reason, map[string]any{
				"model_armor.verdict":    "MODEL_ARMOR_BLOCKED",
				"model_armor.provenance": out.Provenance,
			})
		}
		if g.recordPolicy != nil {
			g.recordPolicy(PolicyEvent{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Boundary:  "Model Armor",
				Action:    fmt.Sprintf("%s (%s)", rpcMethod, stage),
				Verdict:   "MODEL_ARMOR_BLOCKED",
				Detail:    fmt.Sprintf("%s [template=%s, provenance=%s]", out.Reason, firstNonEmpty(g.templateID, "defense-in-depth"), out.Provenance),
			})
		}
		return out, &ModelArmorBlockError{
			IncidentID: incID,
			Stage:      stage,
			Reason:     out.Reason,
			TemplateID: firstNonEmpty(g.templateID, "defense-in-depth"),
			Provenance: out.Provenance,
		}
	}

	// 3. Inline SDP PII de-identification on non-blocked payloads.
	sanitized := out.SanitizedText
	if sdpSSNRegex.MatchString(sanitized) {
		sanitized = sdpSSNRegex.ReplaceAllString(sanitized, "[REDACTED_SSN]")
		out.Redacted = true
		out.RedactedTypes = appendUniqueStr(out.RedactedTypes, "US_SOCIAL_SECURITY_NUMBER")
	}
	if sdpAPIKeyRegex.MatchString(sanitized) {
		sanitized = sdpAPIKeyRegex.ReplaceAllString(sanitized, "[REDACTED_API_KEY]")
		out.Redacted = true
		out.RedactedTypes = appendUniqueStr(out.RedactedTypes, "API_KEY")
	}
	if sdpEmailRegex.MatchString(sanitized) {
		replaced := sdpEmailRegex.ReplaceAllStringFunc(sanitized, func(m string) string {
			if strings.EqualFold(m, "sre-actor@example.com") {
				return m
			}
			out.Redacted = true
			out.RedactedTypes = appendUniqueStr(out.RedactedTypes, "EMAIL_ADDRESS")
			return "[REDACTED_EMAIL]"
		})
		sanitized = replaced
	}
	out.SanitizedText = sanitized

	if out.Redacted {
		g.redactedCount.Add(1)
		if g.server != nil && g.server.metrics != nil {
			g.server.metrics.ObserveModelArmor("redacted")
		}
		if finishSpan != nil {
			finishSpan("OK", "", map[string]any{
				"model_armor.verdict":    "REDACTED",
				"model_armor.provenance": out.Provenance,
			})
		}
		if g.recordPolicy != nil {
			g.recordPolicy(PolicyEvent{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Boundary:  "Model Armor · SDP",
				Action:    fmt.Sprintf("%s (%s)", rpcMethod, stage),
				Verdict:   "REDACTED",
				Detail:    fmt.Sprintf("Inline SDP de-identified %s before crossing Actor Boundary", strings.Join(out.RedactedTypes, ", ")),
			})
		}
		return out, nil
	}
	if g.server != nil && g.server.metrics != nil {
		g.server.metrics.ObserveModelArmor("allow")
	}
	if finishSpan != nil {
		finishSpan("OK", "", map[string]any{
			"model_armor.verdict":    "ALLOW",
			"model_armor.provenance": out.Provenance,
		})
	}
	return out, nil
}

func (g *modelArmorGuard) callCloudModelArmor(ctx context.Context, rpcMethod, dataField, text string) (blocked bool, reason, sdpText string, ok bool) {
	reqCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	bodyBytes, err := json.Marshal(map[string]any{
		dataField: map[string]any{
			"text": text,
		},
	})
	if err != nil {
		return false, "", "", false
	}

	endpoint := g.templateResourceURL() + ":" + rpcMethod
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return false, "", "", false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.liveClient.Do(req)
	if err != nil {
		return false, "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, "", "", false
	}

	var parsed struct {
		SanitizationResult struct {
			FilterMatchState string                     `json:"filterMatchState"`
			FilterResults    map[string]json.RawMessage `json:"filterResults"`
		} `json:"sanitizationResult"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed); err != nil {
		return false, "", "", false
	}
	if parsed.SanitizationResult.FilterMatchState != "MATCH_FOUND" {
		return false, "", "", true
	}

	// Inspect per-filter details when returned so SDP-only de-identification does not block benign prompts.
	if len(parsed.SanitizationResult.FilterResults) > 0 {
		adversarialMatch := false
		var matchedFilters []string
		for k, rawFilter := range parsed.SanitizationResult.FilterResults {
			rawStr := string(rawFilter)
			if !strings.Contains(rawStr, `"MATCH_FOUND"`) {
				continue
			}
			if k == "sdp" {
				var sdpWrapper struct {
					SDPFilterResult struct {
						DeidentifyResult struct {
							Data struct {
								Text string `json:"text"`
							} `json:"data"`
						} `json:"deidentifyResult"`
					} `json:"sdpFilterResult"`
				}
				if json.Unmarshal(rawFilter, &sdpWrapper) == nil && sdpWrapper.SDPFilterResult.DeidentifyResult.Data.Text != "" {
					sdpText = sdpWrapper.SDPFilterResult.DeidentifyResult.Data.Text
				}
				continue
			}
			adversarialMatch = true
			matchedFilters = append(matchedFilters, k)
		}
		if !adversarialMatch {
			return false, "", sdpText, true
		}
		return true, fmt.Sprintf("Model Armor template %q matched %s", g.templateID, strings.Join(matchedFilters, ", ")), sdpText, true
	}

	return true, fmt.Sprintf("Model Armor template %q returned filterMatchState=MATCH_FOUND", g.templateID), "", true
}

func appendUniqueStr(list []string, item string) []string {
	for _, existing := range list {
		if existing == item {
			return list
		}
	}
	return append(list, item)
}
