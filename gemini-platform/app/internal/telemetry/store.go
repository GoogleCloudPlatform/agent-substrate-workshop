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
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/httptransport"
)

const maxStoredTraces = 128

// WaterfallTrace aggregates all scrubbed spans sharing a W3C trace_id across the
// Governed Broker and Incident Actor, annotated with waterfall bar percentages.
type WaterfallTrace struct {
	TraceID              string       `json:"trace_id"`
	IncidentID           string       `json:"incident_id"`
	ActorName            string       `json:"actor_name"`
	Atespace             string       `json:"atespace"`
	SessionID            string       `json:"session_id"`
	Template             string       `json:"template"`
	AgentURN             string       `json:"agent_urn"`
	Status               string       `json:"status"`
	StartTime            string       `json:"start_time"`
	EndTime              string       `json:"end_time"`
	DurationMs           float64      `json:"duration_ms"`
	SpanCount            int          `json:"span_count"`
	InputTokens          int64        `json:"input_tokens"`
	OutputTokens         int64        `json:"output_tokens"`
	ExportedToCloudTrace bool         `json:"exported_to_cloud_trace"`
	Spans                []SpanRecord `json:"spans"`
	ToolSteps            any          `json:"tool_steps,omitempty"`
}

// StoreConfig configures the broker's distributed trace store and Cloud Trace exporter.
type StoreConfig struct {
	ProjectID      string
	Region         string
	Atespace       string
	AgentURN       string
	DefaultModel   string
	MockLLM        bool
	UpstreamURL    string
	ExtraSecrets   []string
	Metrics        *MetricsRegistry
}

type storedTraceEntry struct {
	traceID      string
	incidentID   string
	actorName    string
	atespace     string
	sessionID    string
	template     string
	agentURN     string
	exported     bool
	updatedAt    time.Time
	spansByID    map[string]SpanRecord
	toolSteps    any
}

// Store indexes scrubbed OpenTelemetry GenAI spans on sre-gateway and exports
// batches to Google Cloud Trace v2 (traces:batchWrite).
type Store struct {
	cfg          StoreConfig
	cloudClient  *http.Client
	cloudBaseURL string

	exportedSpans atomic.Int64
	exportErrors  atomic.Int64
	scrubbedTotal atomic.Int64

	mu         sync.RWMutex
	traces     map[string]*storedTraceEntry
	traceOrder []string
}

// NewStore initializes a Store and configures Workload Identity Cloud Trace export
// when UpstreamURL is set or MockLLM is false.
func NewStore(_ context.Context, cfg StoreConfig) *Store {
	proj := strings.TrimSpace(cfg.ProjectID)
	reg := strings.TrimSpace(cfg.Region)
	if reg == "" {
		reg = "us-central1"
	}
	urn := strings.TrimSpace(cfg.AgentURN)
	if urn == "" {
		p := proj
		if p == "" {
			p = "local-dev"
		}
		urn = fmt.Sprintf("projects/%s/locations/%s/agents/%s", p, reg, DefaultAgentName)
	}
	cfg.AgentURN = urn
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = "gemini-3.8-flash"
	}

	s := &Store{
		cfg:    cfg,
		traces: make(map[string]*storedTraceEntry),
	}

	up := strings.TrimRight(strings.TrimSpace(cfg.UpstreamURL), "/")
	if up != "" {
		s.cloudBaseURL = up
		s.cloudClient = &http.Client{Timeout: 10 * time.Second}
	} else if !cfg.MockLLM && proj != "" && proj != "local-dev" {
		if creds, err := credentials.DetectDefault(&credentials.DetectOptions{
			Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
		}); err == nil {
			if hc, err := httptransport.NewClient(&httptransport.Options{
				Credentials: creds,
			}); err == nil {
				hc.Timeout = 10 * time.Second
				s.cloudBaseURL = "https://cloudtrace.googleapis.com"
				s.cloudClient = hc
			}
		}
	}
	return s
}

// CloudTraceEnabled reports whether spans are exported to Cloud Trace v2.
func (s *Store) CloudTraceEnabled() bool {
	return s.cloudClient != nil && s.cfg.ProjectID != "" && s.cfg.ProjectID != "local-dev"
}

// AgentURN returns the Agent Registry URN stamped on gen_ai.agent.id.
func (s *Store) AgentURN() string { return s.cfg.AgentURN }

// ExportedSpanCount returns the total count of spans written to Cloud Trace v2.
func (s *Store) ExportedSpanCount() int64 { return s.exportedSpans.Load() }

// ExportErrorCount returns the total count of failed Cloud Trace v2 batch writes.
func (s *Store) ExportErrorCount() int64 { return s.exportErrors.Load() }

// ScrubbedCount returns the total count of sensitive/payload attributes scrubbed.
func (s *Store) ScrubbedCount() int64 { return s.scrubbedTotal.Load() }

// AssociateTurnMetadata binds incident, actor, session, template, and optional toolSteps to traceID.
func (s *Store) AssociateTurnMetadata(traceID, incidentID, actorName, sessionID, template string, toolSteps any) {
	tid, ok := NormalizeTraceID(traceID)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ent := s.ensureEntryLocked(tid)
	if incidentID != "" {
		ent.incidentID = strings.ToUpper(strings.TrimSpace(incidentID))
	}
	if actorName != "" {
		ent.actorName = strings.TrimSpace(actorName)
	}
	if sessionID != "" {
		ent.sessionID = strings.TrimSpace(sessionID)
	}
	if template != "" {
		ent.template = strings.TrimSpace(template)
	}
	if toolSteps != nil {
		ent.toolSteps = toolSteps
	}
	ent.updatedAt = time.Now().UTC()

	// Backfill gen_ai.* and ate.* metadata onto spans already ingested for this trace_id.
	stamp := s.stampForEntryLocked(ent)
	for id, sp := range ent.spansByID {
		ent.spansByID[id] = StampGenAIAttributes(sp, stamp)
	}
}

// RecordSpans scrubs, stamps, stores, and exports a batch of spans.
func (s *Store) RecordSpans(ctx context.Context, spans []SpanRecord) {
	if len(spans) == 0 {
		return
	}
	cleaned := make([]SpanRecord, 0, len(spans))
	var scrubbedDelta int64

	s.mu.Lock()
	for _, raw := range spans {
		tid, ok := NormalizeTraceID(raw.TraceID)
		if !ok {
			continue
		}
		raw.TraceID = tid
		if !isLowerHex(strings.ToLower(raw.SpanID), 16) || raw.SpanID == zeroSpanID {
			raw.SpanID = NewSpanID()
		} else {
			raw.SpanID = strings.ToLower(raw.SpanID)
		}
		if raw.ParentSpanID != "" {
			if isLowerHex(strings.ToLower(raw.ParentSpanID), 16) && raw.ParentSpanID != zeroSpanID {
				raw.ParentSpanID = strings.ToLower(raw.ParentSpanID)
			} else {
				raw.ParentSpanID = ""
			}
		}

		scrubbed, nScrub := ScrubSpan(raw, s.cfg.ExtraSecrets...)
		scrubbedDelta += int64(nScrub)

		ent := s.ensureEntryLocked(tid)
		// Infer metadata from span attributes if the trace entry hasn't been stamped yet.
		if v, ok := scrubbed.Attributes["gen_ai.conversation.id"].(string); ok && v != "" && ent.sessionID == "" {
			ent.sessionID = v
		}
		if v, ok := scrubbed.Attributes["ate.actor.id"].(string); ok && v != "" && ent.actorName == "" {
			ent.actorName = v
		}
		if v, ok := scrubbed.Attributes["ate.actor.template"].(string); ok && v != "" && ent.template == "" {
			ent.template = v
		}
		if v, ok := scrubbed.Attributes["incident.id"].(string); ok && v != "" && ent.incidentID == "" {
			ent.incidentID = strings.ToUpper(v)
		}

		stamped := StampGenAIAttributes(scrubbed, s.stampForEntryLocked(ent))
		ent.spansByID[stamped.SpanID] = stamped
		ent.updatedAt = time.Now().UTC()
		cleaned = append(cleaned, stamped)
	}
	s.mu.Unlock()

	if scrubbedDelta > 0 {
		s.scrubbedTotal.Add(scrubbedDelta)
		if s.cfg.Metrics != nil {
			s.cfg.Metrics.AddScrubbedAttributes(scrubbedDelta)
		}
	}
	if s.cfg.Metrics != nil && len(cleaned) > 0 {
		s.cfg.Metrics.AddExportedSpans("broker_store", int64(len(cleaned)))
	}

	if s.CloudTraceEnabled() && len(cleaned) > 0 {
		if err := s.exportBatchToCloudTrace(ctx, cleaned); err == nil {
			s.exportedSpans.Add(int64(len(cleaned)))
			if s.cfg.Metrics != nil {
				s.cfg.Metrics.AddExportedSpans("cloud_trace", int64(len(cleaned)))
			}
			s.mu.Lock()
			for _, sp := range cleaned {
				if ent, ok := s.traces[sp.TraceID]; ok {
					ent.exported = true
				}
			}
			s.mu.Unlock()
		} else {
			s.exportErrors.Add(1)
			if s.cfg.Metrics != nil {
				s.cfg.Metrics.AddExportErrors("cloud_trace", 1)
			}
			log.Printf("telemetry: Cloud Trace v2 export failed: %v", err)
		}
	}
}

func (s *Store) ensureEntryLocked(traceID string) *storedTraceEntry {
	if ent, ok := s.traces[traceID]; ok {
		return ent
	}
	if len(s.traceOrder) >= maxStoredTraces {
		evict := s.traceOrder[0]
		s.traceOrder = s.traceOrder[1:]
		delete(s.traces, evict)
	}
	ent := &storedTraceEntry{
		traceID:   traceID,
		atespace:  s.cfg.Atespace,
		agentURN:  s.cfg.AgentURN,
		updatedAt: time.Now().UTC(),
		spansByID: make(map[string]SpanRecord),
	}
	s.traces[traceID] = ent
	s.traceOrder = append(s.traceOrder, traceID)
	return ent
}

func (s *Store) stampForEntryLocked(ent *storedTraceEntry) StampContext {
	return StampContext{
		AgentURN:  s.cfg.AgentURN,
		AgentName: DefaultAgentName,
		SessionID: ent.sessionID,
		ActorID:   ent.actorName,
		Atespace:  s.cfg.Atespace,
		Template:  ent.template,
		ModelName: s.cfg.DefaultModel,
	}
}

// GetTrace returns the assembled WaterfallTrace for traceID.
func (s *Store) GetTrace(traceID string) (WaterfallTrace, bool) {
	tid, ok := NormalizeTraceID(traceID)
	if !ok {
		return WaterfallTrace{}, false
	}
	s.mu.RLock()
	ent, exists := s.traces[tid]
	if !exists || len(ent.spansByID) == 0 {
		s.mu.RUnlock()
		return WaterfallTrace{}, false
	}
	wf := buildWaterfallLocked(ent)
	s.mu.RUnlock()
	return wf, true
}

// ListTraces returns recent WaterfallTraces ordered newest first, optionally filtered by incidentID or sessionID.
func (s *Store) ListTraces(incidentFilter, sessionFilter string) []WaterfallTrace {
	incNorm := strings.ToUpper(strings.TrimSpace(incidentFilter))
	sessNorm := strings.TrimSpace(sessionFilter)

	s.mu.RLock()
	out := make([]WaterfallTrace, 0, len(s.traceOrder))
	for i := len(s.traceOrder) - 1; i >= 0; i-- {
		ent, ok := s.traces[s.traceOrder[i]]
		if !ok || len(ent.spansByID) == 0 {
			continue
		}
		if incNorm != "" && !strings.EqualFold(ent.incidentID, incNorm) {
			continue
		}
		if sessNorm != "" && ent.sessionID != sessNorm {
			continue
		}
		out = append(out, buildWaterfallLocked(ent))
	}
	s.mu.RUnlock()
	return out
}

func buildWaterfallLocked(ent *storedTraceEntry) WaterfallTrace {
	spans := make([]SpanRecord, 0, len(ent.spansByID))
	var (
		minStart   int64
		maxEnd     int64
		inTokens   int64
		outTokens  int64
		traceError bool
	)
	for _, sp := range ent.spansByID {
		if minStart == 0 || sp.StartTimeUnix < minStart {
			minStart = sp.StartTimeUnix
		}
		if sp.EndTimeUnix > maxEnd {
			maxEnd = sp.EndTimeUnix
		}
		if strings.EqualFold(sp.Status, "ERROR") {
			traceError = true
		}
		if sp.Name == "genai.generate_content" {
			inTokens += attrInt64(sp.Attributes, "gen_ai.usage.input_tokens")
			outTokens += attrInt64(sp.Attributes, "gen_ai.usage.output_tokens")
		}
		spans = append(spans, sp)
	}

	slices.SortStableFunc(spans, func(a, b SpanRecord) int {
		if c := cmp.Compare(a.StartTimeUnix, b.StartTimeUnix); c != 0 {
			return c
		}
		return cmp.Compare(a.SpanID, b.SpanID)
	})

	parentMap := make(map[string]string, len(spans))
	for _, sp := range spans {
		parentMap[sp.SpanID] = sp.ParentSpanID
	}

	totalNano := maxEnd - minStart
	if totalNano <= 0 {
		totalNano = 1_000_000 // 1ms floor to avoid division by zero
	}
	for i := range spans {
		off := float64(spans[i].StartTimeUnix-minStart) / float64(totalNano) * 100.0
		wid := float64(spans[i].EndTimeUnix-spans[i].StartTimeUnix) / float64(totalNano) * 100.0
		if off < 0 {
			off = 0
		}
		if off > 98.5 {
			off = 98.5
		}
		if wid < 1.5 {
			wid = 1.5
		}
		if off+wid > 100.0 {
			wid = 100.0 - off
		}
		spans[i].OffsetPct = math.Round(off*10) / 10
		spans[i].WidthPct = math.Round(wid*10) / 10
		spans[i].Depth = computeSpanDepth(spans[i].SpanID, parentMap)
	}

	status := "OK"
	if traceError {
		status = "ERROR"
	}
	incID := ent.incidentID
	if incID == "" && ent.actorName != "" {
		incID = strings.ToUpper(ent.actorName)
	}

	return WaterfallTrace{
		TraceID:              ent.traceID,
		IncidentID:           incID,
		ActorName:            ent.actorName,
		Atespace:             ent.atespace,
		SessionID:            ent.sessionID,
		Template:             ent.template,
		AgentURN:             ent.agentURN,
		Status:               status,
		StartTime:            time.Unix(0, minStart).UTC().Format(time.RFC3339Nano),
		EndTime:              time.Unix(0, maxEnd).UTC().Format(time.RFC3339Nano),
		DurationMs:           math.Round((float64(totalNano)/1e6)*100) / 100,
		SpanCount:            len(spans),
		InputTokens:          inTokens,
		OutputTokens:         outTokens,
		ExportedToCloudTrace: ent.exported,
		Spans:                spans,
		ToolSteps:            ent.toolSteps,
	}
}

func computeSpanDepth(spanID string, parentMap map[string]string) int {
	depth := 0
	cur := spanID
	seen := map[string]bool{cur: true}
	for depth < 8 {
		p := parentMap[cur]
		if p == "" || seen[p] {
			break
		}
		if _, exists := parentMap[p]; !exists {
			break
		}
		seen[p] = true
		depth++
		cur = p
	}
	return depth
}

func attrInt64(attrs map[string]any, key string) int64 {
	if attrs == nil {
		return 0
	}
	switch v := attrs[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	default:
		return 0
	}
}

func (s *Store) exportBatchToCloudTrace(ctx context.Context, spans []SpanRecord) error {
	ctSpans := make([]map[string]any, 0, len(spans))
	for _, sp := range spans {
		startT := time.Unix(0, sp.StartTimeUnix).UTC()
		endT := time.Unix(0, sp.EndTimeUnix).UTC()
		if !endT.After(startT) {
			endT = startT.Add(10 * time.Microsecond)
		}

		attrMap := make(map[string]any, len(sp.Attributes)+2)
		if sp.Boundary != "" {
			attrMap["substrate.boundary"] = cloudTraceStringVal(sp.Boundary)
		}
		if sp.Service != "" {
			attrMap["service.name"] = cloudTraceStringVal(sp.Service)
		}
		for k, v := range sp.Attributes {
			switch val := v.(type) {
			case string:
				attrMap[k] = cloudTraceStringVal(val)
			case bool:
				attrMap[k] = map[string]any{"boolValue": val}
			case int:
				attrMap[k] = map[string]any{"intValue": strconv.Itoa(val)}
			case int64:
				attrMap[k] = map[string]any{"intValue": strconv.FormatInt(val, 10)}
			case float64:
				attrMap[k] = cloudTraceStringVal(strconv.FormatFloat(val, 'f', -1, 64))
			}
		}

		code := 0
		if strings.EqualFold(sp.Status, "ERROR") {
			code = 2
		}
		display := sp.Name
		truncated := 0
		if len(display) > 128 {
			truncated = len(display) - 128
			display = display[:128]
		}
		entry := map[string]any{
			"name":   fmt.Sprintf("projects/%s/traces/%s/spans/%s", s.cfg.ProjectID, sp.TraceID, sp.SpanID),
			"spanId": sp.SpanID,
			"displayName": map[string]any{
				"value":              display,
				"truncatedByteCount": truncated,
			},
			"startTime": startT.Format(time.RFC3339Nano),
			"endTime":   endT.Format(time.RFC3339Nano),
			"attributes": map[string]any{
				"attributeMap": attrMap,
			},
			"status": map[string]any{
				"code":    code,
				"message": truncateUTF8(sp.StatusMessage, 256),
			},
			"spanKind": cloudTraceSpanKind(sp.Kind),
		}
		if sp.ParentSpanID != "" {
			entry["parentSpanId"] = sp.ParentSpanID
		}
		ctSpans = append(ctSpans, entry)
	}

	body, err := json.Marshal(map[string]any{"spans": ctSpans})
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	endpoint := fmt.Sprintf("%s/v2/projects/%s/traces:batchWrite", s.cloudBaseURL, s.cfg.ProjectID)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.cloudClient.Do(req)
	if err != nil {
		return err
	}
	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Cloud Trace batchWrite HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	return nil
}

func cloudTraceStringVal(s string) map[string]any {
	trunc := 0
	if len(s) > 256 {
		trunc = len(s) - 256
		s = truncateUTF8(s, 256)
	}
	return map[string]any{
		"stringValue": map[string]any{
			"value":              s,
			"truncatedByteCount": trunc,
		},
	}
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxBytes], "")
}

func cloudTraceSpanKind(kind string) string {
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "SERVER":
		return "SERVER"
	case "CLIENT":
		return "CLIENT"
	case "PRODUCER":
		return "PRODUCER"
	case "CONSUMER":
		return "CONSUMER"
	default:
		return "INTERNAL"
	}
}

// MetricsRegistry maintains ActorTemplate-scoped low-cardinality Prometheus metrics
// for Google Cloud Managed Service for Prometheus (GMP PodMonitoring).
// Session IDs, Actor IDs, and Trace IDs are intentionally excluded from metric labels.
type MetricsRegistry struct {
	mu sync.RWMutex

	turnsTotal        map[string]int64     // "template|outcome"
	turnDurBuckets    map[string][]int64   // "template" -> bucket counts
	turnDurSum        map[string]float64   // "template"
	turnDurCount      map[string]int64     // "template"
	genaiReqsTotal    map[string]int64     // "model|status"
	genaiTokensTotal  map[string]int64     // "model|direction"
	armorVerdicts     map[string]int64     // "verdict"
	mcpToolCalls      map[string]int64     // "server|verdict"
	spansExported     map[string]int64     // "destination"
	exportErrors      map[string]int64     // "destination"
	evalScores        map[string]float64   // "rubric"
	evalRunsTotal     map[string]int64     // "verdict"
	scrubbedAttrs     atomic.Int64
}

var promDurationBounds = []float64{0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0}

// NewMetricsRegistry constructs a zero-dependency Prometheus metrics registry.
func NewMetricsRegistry() *MetricsRegistry {
	return &MetricsRegistry{
		turnsTotal:       make(map[string]int64),
		turnDurBuckets:   make(map[string][]int64),
		turnDurSum:       make(map[string]float64),
		turnDurCount:     make(map[string]int64),
		genaiReqsTotal:   make(map[string]int64),
		genaiTokensTotal: make(map[string]int64),
		armorVerdicts:    make(map[string]int64),
		mcpToolCalls:     make(map[string]int64),
		spansExported:    make(map[string]int64),
		exportErrors:     make(map[string]int64),
		evalScores:       make(map[string]float64),
		evalRunsTotal:    make(map[string]int64),
	}
}

// ObserveTurn records one gateway turn outcome and duration against template.
func (m *MetricsRegistry) ObserveTurn(template, outcome string, dur time.Duration) {
	tmpl := sanitizePromLabel(template, "sre-agent-v1")
	out := sanitizePromLabel(outcome, "ok")
	sec := dur.Seconds()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.turnsTotal[tmpl+"|"+out]++
	buckets, ok := m.turnDurBuckets[tmpl]
	if !ok {
		buckets = make([]int64, len(promDurationBounds))
		m.turnDurBuckets[tmpl] = buckets
	}
	for i, b := range promDurationBounds {
		if sec <= b {
			buckets[i]++
		}
	}
	m.turnDurSum[tmpl] += sec
	m.turnDurCount[tmpl]++
}

// ObserveGenAI records model request count and input/output token usage.
func (m *MetricsRegistry) ObserveGenAI(modelName, status string, inputTokens, outputTokens int64) {
	mod := sanitizePromLabel(modelName, "gemini-3.8-flash")
	st := sanitizePromLabel(status, "ok")

	m.mu.Lock()
	defer m.mu.Unlock()
	m.genaiReqsTotal[mod+"|"+st]++
	if inputTokens > 0 {
		m.genaiTokensTotal[mod+"|input"] += inputTokens
	}
	if outputTokens > 0 {
		m.genaiTokensTotal[mod+"|output"] += outputTokens
	}
}

// ObserveModelArmor records a Model Armor verdict (allow, redacted, blocked).
func (m *MetricsRegistry) ObserveModelArmor(verdict string) {
	v := sanitizePromLabel(strings.ToLower(verdict), "allow")
	m.mu.Lock()
	m.armorVerdicts[v]++
	m.mu.Unlock()
}

// ObserveMCPCall records an MCP broker call outcome by server ID and verdict.
func (m *MetricsRegistry) ObserveMCPCall(serverID, verdict string) {
	srv := sanitizePromLabel(serverID, "unknown")
	v := sanitizePromLabel(strings.ToLower(verdict), "allow")
	m.mu.Lock()
	m.mcpToolCalls[srv+"|"+v]++
	m.mu.Unlock()
}

// AddExportedSpans increments the exported span counter for destination.
func (m *MetricsRegistry) AddExportedSpans(destination string, n int64) {
	if n <= 0 {
		return
	}
	dest := sanitizePromLabel(destination, "broker_store")
	m.mu.Lock()
	m.spansExported[dest] += n
	m.mu.Unlock()
}

// AddExportErrors increments the span export error counter for destination.
func (m *MetricsRegistry) AddExportErrors(destination string, n int64) {
	if n <= 0 {
		return
	}
	dest := sanitizePromLabel(destination, "cloud_trace")
	m.mu.Lock()
	m.exportErrors[dest] += n
	m.mu.Unlock()
}

// AddScrubbedAttributes increments the scrubbed attribute counter.
func (m *MetricsRegistry) AddScrubbedAttributes(n int64) {
	if n > 0 {
		m.scrubbedAttrs.Add(n)
	}
}

// ObserveEvalRun records online evaluation rubric gauges and overall run verdict.
func (m *MetricsRegistry) ObserveEvalRun(scores map[string]float64, passed bool) {
	verdict := "fail"
	if passed {
		verdict = "pass"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evalRunsTotal[verdict]++
	for k, v := range scores {
		m.evalScores[sanitizePromLabel(k, "rubric")] = v
	}
}

// RenderText formats all registered metrics in Prometheus 0.0.4 exposition format.
func (m *MetricsRegistry) RenderText() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var b strings.Builder

	b.WriteString("# HELP sre_gateway_turns_total Total incident turns handled by sre-gateway, partitioned by ActorTemplate and outcome.\n")
	b.WriteString("# TYPE sre_gateway_turns_total counter\n")
	for _, k := range sortedKeys(m.turnsTotal) {
		tmpl, out, _ := strings.Cut(k, "|")
		fmt.Fprintf(&b, "sre_gateway_turns_total{template=%q,outcome=%q} %d\n", tmpl, out, m.turnsTotal[k])
	}

	b.WriteString("# HELP sre_gateway_turn_duration_seconds End-to-end gateway turn duration in seconds by ActorTemplate.\n")
	b.WriteString("# TYPE sre_gateway_turn_duration_seconds histogram\n")
	for _, tmpl := range sortedKeys(m.turnDurCount) {
		buckets := m.turnDurBuckets[tmpl]
		for i, bound := range promDurationBounds {
			var cnt int64
			if i < len(buckets) {
				cnt = buckets[i]
			}
			fmt.Fprintf(&b, "sre_gateway_turn_duration_seconds_bucket{template=%q,le=%q} %d\n", tmpl, strconv.FormatFloat(bound, 'f', -1, 64), cnt)
		}
		fmt.Fprintf(&b, "sre_gateway_turn_duration_seconds_bucket{template=%q,le=\"+Inf\"} %d\n", tmpl, m.turnDurCount[tmpl])
		fmt.Fprintf(&b, "sre_gateway_turn_duration_seconds_sum{template=%q} %.6f\n", tmpl, m.turnDurSum[tmpl])
		fmt.Fprintf(&b, "sre_gateway_turn_duration_seconds_count{template=%q} %d\n", tmpl, m.turnDurCount[tmpl])
	}

	b.WriteString("# HELP sre_gateway_genai_requests_total Outbound Gemini platform calls proxied by sre-gateway.\n")
	b.WriteString("# TYPE sre_gateway_genai_requests_total counter\n")
	for _, k := range sortedKeys(m.genaiReqsTotal) {
		mod, st, _ := strings.Cut(k, "|")
		fmt.Fprintf(&b, "sre_gateway_genai_requests_total{model=%q,status=%q} %d\n", mod, st, m.genaiReqsTotal[k])
	}

	b.WriteString("# HELP sre_gateway_genai_tokens_total Total Gemini input and output tokens observed by sre-gateway.\n")
	b.WriteString("# TYPE sre_gateway_genai_tokens_total counter\n")
	for _, k := range sortedKeys(m.genaiTokensTotal) {
		mod, dir, _ := strings.Cut(k, "|")
		fmt.Fprintf(&b, "sre_gateway_genai_tokens_total{model=%q,direction=%q} %d\n", mod, dir, m.genaiTokensTotal[k])
	}

	b.WriteString("# HELP sre_gateway_model_armor_verdicts_total Model Armor prompt and response screening verdicts.\n")
	b.WriteString("# TYPE sre_gateway_model_armor_verdicts_total counter\n")
	for _, v := range sortedKeys(m.armorVerdicts) {
		fmt.Fprintf(&b, "sre_gateway_model_armor_verdicts_total{verdict=%q} %d\n", v, m.armorVerdicts[v])
	}

	b.WriteString("# HELP sre_gateway_mcp_tool_calls_total Governed MCP tool calls evaluated by sre-gateway.\n")
	b.WriteString("# TYPE sre_gateway_mcp_tool_calls_total counter\n")
	for _, k := range sortedKeys(m.mcpToolCalls) {
		srv, v, _ := strings.Cut(k, "|")
		fmt.Fprintf(&b, "sre_gateway_mcp_tool_calls_total{server=%q,verdict=%q} %d\n", srv, v, m.mcpToolCalls[k])
	}

	b.WriteString("# HELP sre_gateway_otel_spans_exported_total Scrubbed OpenTelemetry GenAI spans stored and exported.\n")
	b.WriteString("# TYPE sre_gateway_otel_spans_exported_total counter\n")
	for _, dest := range sortedKeys(m.spansExported) {
		fmt.Fprintf(&b, "sre_gateway_otel_spans_exported_total{destination=%q} %d\n", dest, m.spansExported[dest])
	}

	b.WriteString("# HELP sre_gateway_otel_export_errors_total Failed OpenTelemetry span batch exports by destination.\n")
	b.WriteString("# TYPE sre_gateway_otel_export_errors_total counter\n")
	for _, dest := range sortedKeys(m.exportErrors) {
		fmt.Fprintf(&b, "sre_gateway_otel_export_errors_total{destination=%q} %d\n", dest, m.exportErrors[dest])
	}

	b.WriteString("# HELP sre_gateway_otel_attributes_scrubbed_total Sensitive headers, tokens, PII, and raw payload attributes scrubbed before trace export.\n")
	b.WriteString("# TYPE sre_gateway_otel_attributes_scrubbed_total counter\n")
	fmt.Fprintf(&b, "sre_gateway_otel_attributes_scrubbed_total %d\n", m.scrubbedAttrs.Load())

	b.WriteString("# HELP sre_gateway_online_eval_score Latest pure-Go online evaluation rubric score (0.0 to 1.0).\n")
	b.WriteString("# TYPE sre_gateway_online_eval_score gauge\n")
	for _, rub := range sortedKeys(m.evalScores) {
		fmt.Fprintf(&b, "sre_gateway_online_eval_score{rubric=%q} %.4f\n", rub, m.evalScores[rub])
	}

	b.WriteString("# HELP sre_gateway_online_eval_runs_total Online evaluation runs executed by sre-gateway.\n")
	b.WriteString("# TYPE sre_gateway_online_eval_runs_total counter\n")
	for _, v := range sortedKeys(m.evalRunsTotal) {
		fmt.Fprintf(&b, "sre_gateway_online_eval_runs_total{verdict=%q} %d\n", v, m.evalRunsTotal[v])
	}

	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sanitizePromLabel(raw, fallback string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return fallback
	}
	var b strings.Builder
	for i := 0; i < len(v) && b.Len() < 64; i++ {
		c := v[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return fallback
	}
	return b.String()
}
