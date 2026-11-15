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
	"io"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/gke-demos/substrate-agent-platform/app/internal/telemetry"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

const maxOTLPTraceBytes = 4 << 20 // 4 MiB ceiling per OTLP trace batch.

func (s *Server) startRootTurnSpan(
	ctx context.Context,
	incID, actorName, sessID, tmpl string,
) (context.Context, telemetry.SpanContext, func(status, msg string, extra map[string]any)) {
	start := time.Now().UTC()
	parentSC, hasParent := telemetry.SpanContextFromContext(ctx)
	traceID := parentSC.TraceID
	parentSpanID := ""
	if hasParent {
		parentSpanID = parentSC.SpanID
	} else {
		traceID = telemetry.NewTraceID()
	}
	rootSpanID := telemetry.NewSpanID()
	rootSC := telemetry.SpanContext{
		TraceID:    traceID,
		SpanID:     rootSpanID,
		Flags:      "01",
		TraceState: parentSC.TraceState,
	}
	nextCtx := telemetry.ContextWithSpanContext(ctx, rootSC)

	s.traces.AssociateTurnMetadata(traceID, incID, actorName, sessID, tmpl, nil)
	s.setActiveActorTrace(actorName, rootSC)

	finish := func(status, msg string, extra map[string]any) {
		s.clearActiveActorTrace(actorName, traceID)
		attrs := map[string]any{
			"gen_ai.operation.name": "invoke_gateway_turn",
			"gen_ai.conversation.id": sessID,
			"ate.actor.id":           actorName,
			"ate.atespace":           s.cfg.Atespace,
			"ate.actor.template":     tmpl,
			"incident.id":            incID,
		}
		for k, v := range extra {
			attrs[k] = v
		}
		rec := telemetry.NewSpanRecord(
			traceID,
			rootSpanID,
			parentSpanID,
			"invoke_gateway_turn",
			"SERVER",
			"Governed Broker",
			"sre-gateway",
			start,
			time.Now().UTC(),
			status,
			msg,
			attrs,
		)
		s.traces.RecordSpans(ctx, []telemetry.SpanRecord{rec})
	}
	return nextCtx, rootSC, finish
}

func (s *Server) startBrokerSpan(
	ctx context.Context,
	name, kind, boundary string,
	attrs map[string]any,
) (context.Context, telemetry.SpanContext, func(status, msg string, extra map[string]any)) {
	start := time.Now().UTC()
	parentSC, hasParent := telemetry.SpanContextFromContext(ctx)
	if !hasParent {
		if fallbackSC, ok := s.anyActiveActorTrace(); ok {
			parentSC = fallbackSC
			hasParent = true
		}
	}
	traceID := parentSC.TraceID
	parentSpanID := ""
	if hasParent {
		parentSpanID = parentSC.SpanID
	} else {
		traceID = telemetry.NewTraceID()
	}
	spanID := telemetry.NewSpanID()
	sc := telemetry.SpanContext{
		TraceID:    traceID,
		SpanID:     spanID,
		Flags:      "01",
		TraceState: parentSC.TraceState,
	}
	nextCtx := telemetry.ContextWithSpanContext(ctx, sc)

	baseAttrs := maps.Clone(attrs)
	if baseAttrs == nil {
		baseAttrs = make(map[string]any, 4)
	}

	finish := func(status, msg string, extra map[string]any) {
		merged := maps.Clone(baseAttrs)
		for k, v := range extra {
			merged[k] = v
		}
		rec := telemetry.NewSpanRecord(
			traceID,
			spanID,
			parentSpanID,
			name,
			kind,
			boundary,
			"sre-gateway",
			start,
			time.Now().UTC(),
			status,
			msg,
			merged,
		)
		s.traces.RecordSpans(ctx, []telemetry.SpanRecord{rec})
	}
	return nextCtx, sc, finish
}

func (s *Server) setActiveActorTrace(actorName string, sc telemetry.SpanContext) {
	if actorName == "" || !sc.Valid() {
		return
	}
	s.mu.Lock()
	s.activeTraceByActor[actorName] = sc
	s.mu.Unlock()
}

func (s *Server) clearActiveActorTrace(actorName, traceID string) {
	if actorName == "" {
		return
	}
	s.mu.Lock()
	if cur, ok := s.activeTraceByActor[actorName]; ok && cur.TraceID == traceID {
		delete(s.activeTraceByActor, actorName)
	}
	s.mu.Unlock()
}

func (s *Server) anyActiveActorTrace() (telemetry.SpanContext, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sc := range s.activeTraceByActor {
		if sc.Valid() {
			return sc, true
		}
	}
	return telemetry.SpanContext{}, false
}

func (s *Server) isActorTurnTrace(traceID string) bool {
	if strings.TrimSpace(traceID) == "" {
		return false
	}
	s.mu.Lock()
	for _, sc := range s.activeTraceByActor {
		if sc.TraceID == traceID {
			s.mu.Unlock()
			return true
		}
	}
	s.mu.Unlock()
	wf, ok := s.traces.GetTrace(traceID)
	return ok && (strings.TrimSpace(wf.SessionID) != "" || strings.TrimSpace(wf.IncidentID) != "")
}

// handleOTLPTraces ingests keyless OTLP/HTTP protobuf and JSON span batches posted by
// Incident Actors across EgressPolicy, scrubs sensitive attributes/payloads, and forwards
// to Cloud Trace.
func (s *Server) handleOTLPTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxOTLPTraceBytes))
	if err != nil {
		writeBrokerJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "OTLP trace payload exceeds limit"})
		return
	}
	spans, isProto, err := telemetry.DecodeOTLPRequest(raw, r.Header.Get("Content-Type"), r.Header.Get("Content-Encoding"), maxOTLPTraceBytes)
	if err != nil {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid OTLP trace payload"})
		return
	}
	s.traces.RecordSpans(r.Context(), spans)
	if isProto {
		respBytes, marshalErr := proto.Marshal(&coltracepb.ExportTraceServiceResponse{
			PartialSuccess: &coltracepb.ExportTracePartialSuccess{},
		})
		if marshalErr == nil {
			w.Header().Set("Content-Type", "application/x-protobuf")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(respBytes)
			return
		}
	}
	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"partialSuccess": map[string]any{},
		"acceptedSpans":  len(spans),
	})
}

func (s *Server) handleTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	incFilter := strings.TrimSpace(r.URL.Query().Get("incident_id"))
	sessFilter := strings.TrimSpace(r.URL.Query().Get("session_id"))
	list := s.traces.ListTraces(incFilter, sessFilter)

	backend := "broker-waterfall"
	if s.traces.CloudTraceEnabled() {
		backend = "cloud-trace"
	}
	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"backend":              backend,
		"cloud_trace_enabled":  s.traces.CloudTraceEnabled(),
		"project_id":           firstNonEmpty(s.cfg.ProjectID, "local-dev"),
		"agent_urn":            s.traces.AgentURN(),
		"exported_span_count":  s.traces.ExportedSpanCount(),
		"scrubbed_count":       s.traces.ScrubbedCount(),
		"traces":               list,
	})
}

func (s *Server) handleTraceSubroutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	traceID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/traces/"))
	if traceID == "" || strings.Contains(traceID, "/") {
		http.NotFound(w, r)
		return
	}
	wf, ok := s.traces.GetTrace(traceID)
	if !ok {
		writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": "trace not found"})
		return
	}
	writeBrokerJSON(w, http.StatusOK, wf)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, s.metrics.RenderText())
	}
}
