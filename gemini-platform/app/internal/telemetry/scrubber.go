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

package telemetry

import (
	"maps"
	"regexp"
	"strings"
)

const (
	// GenAISystemValue identifies Gemini platform on all GenAI spans.
	GenAISystemValue = "gemini_platform"
	// DefaultAgentName is the canonical gen_ai.agent.name for the workshop agent.
	DefaultAgentName = "platform-sre-triage-agent"
)

var (
	bearerTokenRE = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._\-+/=]{6,}`)
	googleTokenRE = regexp.MustCompile(`\bya29\.[A-Za-z0-9._\-]+`)
	githubTokenRE = regexp.MustCompile(`\b(?:gho|ghu|ghp|ghs|ghr)_[A-Za-z0-9_]+|\bgithub_pat_[A-Za-z0-9_]+`)
	opshubTokenRE = regexp.MustCompile(`\bopshub_(?:at|rt|code)_[A-Za-z0-9._\-]+`)
	apiKeyRE      = regexp.MustCompile(`\b(?:AIza[0-9A-Za-z\-_]{12,}|sk-[A-Za-z0-9]{16,})\b`)
	jwtTokenRE    = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{4,}\b`)
	cookiePairRE  = regexp.MustCompile(`(?i)(?:__Host-)?(?:sre_csrf|opshub_csrf|session_token|access_token|refresh_token)=[^\s;,"']+`)
	ssnValueRE    = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
	emailValueRE  = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
)

// droppedPayloadKeys lists raw tool argument, response, prompt, and body keys that
// are stripped completely from spans before storage or Cloud Trace export.
var droppedPayloadKeys = map[string]bool{
	"gcp.vertex.agent.tool_call_args": true,
	"gcp.vertex.agent.tool_response":  true,
	"gen_ai.prompt":                   true,
	"gen_ai.completion":               true,
	"gen_ai.input.messages":           true,
	"gen_ai.output.messages":          true,
	"gen_ai.system_instructions":      true,
	"gen_ai.tool.call.arguments":      true,
	"gen_ai.tool.call.result":         true,
	"tool.args":                       true,
	"tool.result":                     true,
	"http.request.body":               true,
	"http.response.body":              true,
	"exception.message":               true,
	"exception.stacktrace":            true,
}

var sensitiveKeySubstrings = []string{
	"authorization",
	"cookie",
	"set-cookie",
	"x-api-key",
	"api_key",
	"apikey",
	"client_secret",
	"access_token",
	"refresh_token",
	"id_token",
	"bearer",
	"password",
	"secret",
	"private_key",
	"csrf",
	"workload-access-token",
	"x-broker-admin-token",
	"x-remediation-gate-token",
	"gate_approval_token",
	"x-ledger-reload-token",
}

// SpanRecord represents a scrubbed, JSON-serializable distributed trace span.
type SpanRecord struct {
	TraceID       string         `json:"trace_id"`
	SpanID        string         `json:"span_id"`
	ParentSpanID  string         `json:"parent_span_id,omitempty"`
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	Boundary      string         `json:"boundary"`
	Service       string         `json:"service"`
	StartTimeUnix int64          `json:"start_time_unix_nano"`
	EndTimeUnix   int64          `json:"end_time_unix_nano"`
	StartTime     string         `json:"start_time"`
	EndTime       string         `json:"end_time"`
	DurationMs    float64        `json:"duration_ms"`
	OffsetPct     float64        `json:"offset_pct"`
	WidthPct      float64        `json:"width_pct"`
	Depth         int            `json:"depth"`
	Status        string         `json:"status"`
	StatusMessage string         `json:"status_message,omitempty"`
	Attributes    map[string]any `json:"attributes,omitempty"`
}

// StampContext carries canonical GenAI and Substrate metadata stamped onto a trace's spans.
type StampContext struct {
	AgentURN  string
	AgentName string
	SessionID string
	ActorID   string
	Atespace  string
	Template  string
	ModelName string
}

// ScrubSpan removes raw tool payload blobs, redacts sensitive header/credential keys,
// scrubs bearer tokens and PII from string fields, and returns the scrubbed span
// together with the count of scrubbed items.
func ScrubSpan(sp SpanRecord, extraSecrets ...string) (SpanRecord, int) {
	scrubbed := 0
	out := sp
	if nameClean, n := scrubStringValue(out.Name, extraSecrets); n > 0 {
		out.Name = nameClean
		scrubbed += n
	}
	if msgClean, n := scrubStringValue(out.StatusMessage, extraSecrets); n > 0 {
		out.StatusMessage = msgClean
		scrubbed += n
	}
	if out.Kind == "" {
		out.Kind = "INTERNAL"
	}
	if out.Status == "" {
		out.Status = "OK"
	}

	cleanAttrs := make(map[string]any, len(sp.Attributes))
	for rawKey, rawVal := range sp.Attributes {
		lowerKey := strings.ToLower(strings.TrimSpace(rawKey))
		if lowerKey == "" {
			continue
		}
		if droppedPayloadKeys[lowerKey] {
			scrubbed++
			continue
		}
		if isSensitiveAttrKey(lowerKey) {
			cleanAttrs[rawKey] = "[SCRUBBED]"
			scrubbed++
			continue
		}
		if lowerKey == "gen_ai.system" {
			cleanAttrs["gen_ai.system"] = GenAISystemValue
			continue
		}

		switch v := rawVal.(type) {
		case string:
			cleaned, n := scrubStringValue(v, extraSecrets)
			scrubbed += n
			cleanAttrs[rawKey] = cleaned
		case []string:
			cp := make([]string, len(v))
			for i, item := range v {
				c, n := scrubStringValue(item, extraSecrets)
				scrubbed += n
				cp[i] = c
			}
			cleanAttrs[rawKey] = cp
		case int, int64, int32, float64, bool:
			cleanAttrs[rawKey] = v
		default:
			// Drop arbitrary nested map/slice blobs to guarantee payload-free export.
			scrubbed++
		}
	}
	out.Attributes = cleanAttrs
	return out, scrubbed
}

// StampGenAIAttributes populates OpenTelemetry GenAI and Substrate attributes on sp
// without overwriting span-specific values already present on sp.
func StampGenAIAttributes(sp SpanRecord, meta StampContext) SpanRecord {
	out := sp
	attrs := maps.Clone(sp.Attributes)
	if attrs == nil {
		attrs = make(map[string]any, 8)
	}
	attrs["gen_ai.system"] = GenAISystemValue
	if meta.AgentURN != "" && emptyAttr(attrs, "gen_ai.agent.id") {
		attrs["gen_ai.agent.id"] = meta.AgentURN
	}
	agentName := meta.AgentName
	if agentName == "" {
		agentName = DefaultAgentName
	}
	if emptyAttr(attrs, "gen_ai.agent.name") {
		attrs["gen_ai.agent.name"] = agentName
	}
	if meta.SessionID != "" && emptyAttr(attrs, "gen_ai.conversation.id") {
		attrs["gen_ai.conversation.id"] = meta.SessionID
	}
	if meta.ActorID != "" && emptyAttr(attrs, "ate.actor.id") {
		attrs["ate.actor.id"] = meta.ActorID
	}
	if meta.Atespace != "" && emptyAttr(attrs, "ate.atespace") {
		attrs["ate.atespace"] = meta.Atespace
	}
	if meta.Template != "" && emptyAttr(attrs, "ate.actor.template") {
		attrs["ate.actor.template"] = meta.Template
	}
	if meta.ModelName != "" && emptyAttr(attrs, "gen_ai.request.model") {
		attrs["gen_ai.request.model"] = meta.ModelName
	}
	out.Attributes = attrs
	return out
}

func emptyAttr(attrs map[string]any, key string) bool {
	v, ok := attrs[key]
	if !ok || v == nil {
		return true
	}
	s, isStr := v.(string)
	return isStr && strings.TrimSpace(s) == ""
}

func isSensitiveAttrKey(lowerKey string) bool {
	for _, sub := range sensitiveKeySubstrings {
		if strings.Contains(lowerKey, sub) {
			return true
		}
	}
	return false
}

// ScrubString scrubs credentials, bearer tokens, cookies, and PII from s and
// returns the scrubbed string plus the number of redactions performed.
func ScrubString(in string, extraSecrets []string) (string, int) {
	return scrubStringValue(in, extraSecrets)
}

// CountTokenLeaks counts unredacted bearer/OAuth/API token matches in s without
// counting email or SSN patterns.
func CountTokenLeaks(in string, extraSecrets []string) int {
	if in == "" {
		return 0
	}
	hits := 0
	for _, sec := range extraSecrets {
		trimmed := strings.TrimSpace(sec)
		if len(trimmed) >= 6 && strings.Contains(in, trimmed) {
			hits++
		}
	}
	hits += len(bearerTokenRE.FindAllStringIndex(in, -1))
	hits += len(googleTokenRE.FindAllStringIndex(in, -1))
	hits += len(githubTokenRE.FindAllStringIndex(in, -1))
	hits += len(opshubTokenRE.FindAllStringIndex(in, -1))
	hits += len(apiKeyRE.FindAllStringIndex(in, -1))
	hits += len(jwtTokenRE.FindAllStringIndex(in, -1))
	hits += len(cookiePairRE.FindAllStringIndex(in, -1))
	return hits
}

func scrubStringValue(in string, extraSecrets []string) (string, int) {
	if in == "" {
		return "", 0
	}
	s := in
	count := 0

	for _, sec := range extraSecrets {
		trimmed := strings.TrimSpace(sec)
		if len(trimmed) >= 6 && strings.Contains(s, trimmed) {
			s = strings.ReplaceAll(s, trimmed, "[SCRUBBED_SECRET]")
			count++
		}
	}
	if bearerTokenRE.MatchString(s) {
		s = bearerTokenRE.ReplaceAllString(s, "Bearer [SCRUBBED]")
		count++
	}
	if googleTokenRE.MatchString(s) {
		s = googleTokenRE.ReplaceAllString(s, "[SCRUBBED_TOKEN]")
		count++
	}
	if githubTokenRE.MatchString(s) {
		s = githubTokenRE.ReplaceAllString(s, "[SCRUBBED_TOKEN]")
		count++
	}
	if opshubTokenRE.MatchString(s) {
		s = opshubTokenRE.ReplaceAllString(s, "[SCRUBBED_TOKEN]")
		count++
	}
	if apiKeyRE.MatchString(s) {
		s = apiKeyRE.ReplaceAllString(s, "[SCRUBBED_API_KEY]")
		count++
	}
	if jwtTokenRE.MatchString(s) {
		s = jwtTokenRE.ReplaceAllString(s, "[SCRUBBED_JWT]")
		count++
	}
	if cookiePairRE.MatchString(s) {
		s = cookiePairRE.ReplaceAllString(s, "[SCRUBBED_COOKIE]")
		count++
	}
	if ssnValueRE.MatchString(s) {
		s = ssnValueRE.ReplaceAllString(s, "[REDACTED_SSN]")
		count++
	}
	if emailValueRE.MatchString(s) {
		s = emailValueRE.ReplaceAllStringFunc(s, func(m string) string {
			if strings.EqualFold(m, "sre-actor@example.com") {
				return m
			}
			count++
			return "[REDACTED_EMAIL]"
		})
	}
	return s, count
}
