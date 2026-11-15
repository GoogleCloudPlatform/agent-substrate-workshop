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
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

type bufferCtxKey struct{}

var installOnce sync.Once

// SpanBuffer collects spans recorded on a single request context inside an Incident Actor.
type SpanBuffer struct {
	mu      sync.Mutex
	service string
	meta    StampContext
	spans   []SpanRecord
}

// NewSpanBuffer allocates a request-scoped SpanBuffer for service.
func NewSpanBuffer(service string, meta StampContext) *SpanBuffer {
	return &SpanBuffer{
		service: service,
		meta:    meta,
	}
}

// ContextWithBuffer attaches buf to ctx so the context-routed OpenTelemetry TracerProvider
// records ADK Go v2 spans into buf without cross-request or cross-test contention.
func ContextWithBuffer(ctx context.Context, buf *SpanBuffer) context.Context {
	InstallADKTracerProvider()
	return context.WithValue(ctx, bufferCtxKey{}, buf)
}

// BufferFromContext retrieves the active SpanBuffer from ctx, if any.
func BufferFromContext(ctx context.Context) *SpanBuffer {
	if ctx == nil {
		return nil
	}
	buf, _ := ctx.Value(bufferCtxKey{}).(*SpanBuffer)
	return buf
}

// Add appends a scrubbed, metadata-stamped span to b.
func (b *SpanBuffer) Add(sp SpanRecord) {
	if b == nil {
		return
	}
	if sp.Service == "" {
		sp.Service = b.service
	}
	scrubbed, _ := ScrubSpan(sp)
	stamped := StampGenAIAttributes(scrubbed, b.meta)
	b.mu.Lock()
	b.spans = append(b.spans, stamped)
	b.mu.Unlock()
}

// Drain returns all recorded spans and clears the buffer.
func (b *SpanBuffer) Drain() []SpanRecord {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	out := append([]SpanRecord(nil), b.spans...)
	b.spans = nil
	b.mu.Unlock()
	return out
}

// InstallADKTracerProvider installs a context-routed OpenTelemetry TracerProvider
// once per process so ADK Go v2's internal telemetry package emits invoke_agent and
// execute_tool spans directly into the active turn's SpanBuffer.
func InstallADKTracerProvider() {
	installOnce.Do(func() {
		otel.SetTracerProvider(&contextTracerProvider{})
	})
}

type contextTracerProvider struct {
	embedded.TracerProvider
}

func (p *contextTracerProvider) Tracer(name string, _ ...oteltrace.TracerOption) oteltrace.Tracer {
	return &contextTracer{instName: name}
}

type contextTracer struct {
	embedded.Tracer
	instName string
}

func (t *contextTracer) Start(ctx context.Context, spanName string, opts ...oteltrace.SpanStartOption) (context.Context, oteltrace.Span) {
	buf := BufferFromContext(ctx)
	parentSC, hasParent := SpanContextFromContext(ctx)

	traceID := parentSC.TraceID
	parentSpanID := ""
	if hasParent {
		parentSpanID = parentSC.SpanID
	} else {
		traceID = NewTraceID()
	}
	spanID := NewSpanID()
	childSC := SpanContext{
		TraceID:    traceID,
		SpanID:     spanID,
		Flags:      "01",
		TraceState: parentSC.TraceState,
	}

	cfg := oteltrace.NewSpanStartConfig(opts...)
	startTime := cfg.Timestamp()
	if startTime.IsZero() {
		startTime = time.Now().UTC()
	}

	otelSC, _ := toOTelSpanContext(childSC, false)
	sp := &bufferedOTelSpan{
		buf:          buf,
		sc:           otelSC,
		traceID:      traceID,
		spanID:       spanID,
		parentSpanID: parentSpanID,
		rawName:      spanName,
		kind:         cfg.SpanKind(),
		startTime:    startTime,
		attrs:        make(map[string]any, len(cfg.Attributes())+4),
	}
	for _, kv := range cfg.Attributes() {
		sp.setAttr(kv)
	}

	nextCtx := context.WithValue(ctx, spanCtxKey{}, childSC)
	nextCtx = oteltrace.ContextWithSpan(nextCtx, sp)
	return nextCtx, sp
}

type bufferedOTelSpan struct {
	embedded.Span

	mu           sync.Mutex
	buf          *SpanBuffer
	sc           oteltrace.SpanContext
	traceID      string
	spanID       string
	parentSpanID string
	rawName      string
	kind         oteltrace.SpanKind
	startTime    time.Time
	ended        bool
	statusCode   codes.Code
	statusMsg    string
	attrs        map[string]any
}

func (s *bufferedOTelSpan) End(opts ...oteltrace.SpanEndOption) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	endCfg := oteltrace.NewSpanEndConfig(opts...)
	endTime := endCfg.Timestamp()
	if endTime.IsZero() {
		endTime = time.Now().UTC()
	}
	if endTime.Before(s.startTime) {
		endTime = s.startTime
	}

	canonicalName, boundary, kindStr, keep := classifyADKSpan(s.rawName, s.kind, s.attrs)
	if !keep || s.buf == nil {
		s.mu.Unlock()
		return
	}

	statusStr := "OK"
	if s.statusCode == codes.Error {
		statusStr = "ERROR"
	}
	attrsCopy := make(map[string]any, len(s.attrs))
	for k, v := range s.attrs {
		attrsCopy[k] = v
	}
	s.mu.Unlock()

	rec := NewSpanRecord(s.traceID, s.spanID, s.parentSpanID, canonicalName, kindStr, boundary, s.buf.service, s.startTime, endTime, statusStr, s.statusMsg, attrsCopy)
	s.buf.Add(rec)
}

func classifyADKSpan(rawName string, sk oteltrace.SpanKind, attrs map[string]any) (canonicalName, boundary, kindStr string, keep bool) {
	trimmed := strings.TrimSpace(rawName)
	switch {
	case trimmed == "invoke_agent" || strings.HasPrefix(trimmed, "invoke_agent "):
		attrs["gen_ai.operation.name"] = "invoke_agent"
		return "invoke_agent", "Incident Actor", "SERVER", true
	case trimmed == "execute_tool" || strings.HasPrefix(trimmed, "execute_tool "):
		if toolSuffix := strings.TrimSpace(strings.TrimPrefix(trimmed, "execute_tool")); toolSuffix != "" && emptyAttr(attrs, "gen_ai.tool.name") {
			attrs["gen_ai.tool.name"] = toolSuffix
		}
		attrs["gen_ai.operation.name"] = "execute_tool"
		return "execute_tool", "Incident Actor", "INTERNAL", true
	case strings.HasPrefix(trimmed, "generate_content"):
		// sre-gateway records the governed genai.generate_content CLIENT span with
		// upstream token counts; skip emitting a duplicate inside the sandbox while
		// still propagating this span's context on the HTTP hop.
		return "", "", "", false
	default:
		return trimmed, "Incident Actor", spanKindString(sk), true
	}
}

func spanKindString(sk oteltrace.SpanKind) string {
	switch sk {
	case oteltrace.SpanKindServer:
		return "SERVER"
	case oteltrace.SpanKindClient:
		return "CLIENT"
	case oteltrace.SpanKindProducer:
		return "PRODUCER"
	case oteltrace.SpanKindConsumer:
		return "CONSUMER"
	default:
		return "INTERNAL"
	}
}

func (s *bufferedOTelSpan) AddEvent(string, ...oteltrace.EventOption) {}
func (s *bufferedOTelSpan) AddLink(oteltrace.Link)                    {}

func (s *bufferedOTelSpan) IsRecording() bool {
	return s.buf != nil
}

func (s *bufferedOTelSpan) RecordError(err error, _ ...oteltrace.EventOption) {
	if err == nil {
		return
	}
	s.mu.Lock()
	if s.statusMsg == "" {
		s.statusMsg = err.Error()
	}
	s.mu.Unlock()
}

func (s *bufferedOTelSpan) SpanContext() oteltrace.SpanContext { return s.sc }

func (s *bufferedOTelSpan) SetStatus(code codes.Code, description string) {
	s.mu.Lock()
	s.statusCode = code
	if description != "" {
		s.statusMsg = description
	}
	s.mu.Unlock()
}

func (s *bufferedOTelSpan) SetName(name string) {
	s.mu.Lock()
	s.rawName = name
	s.mu.Unlock()
}

func (s *bufferedOTelSpan) SetAttributes(kv ...attribute.KeyValue) {
	s.mu.Lock()
	for _, item := range kv {
		s.setAttr(item)
	}
	s.mu.Unlock()
}

func (s *bufferedOTelSpan) setAttr(kv attribute.KeyValue) {
	k := string(kv.Key)
	switch kv.Value.Type() {
	case attribute.BOOL:
		s.attrs[k] = kv.Value.AsBool()
	case attribute.INT64:
		s.attrs[k] = kv.Value.AsInt64()
	case attribute.FLOAT64:
		s.attrs[k] = kv.Value.AsFloat64()
	case attribute.STRING:
		s.attrs[k] = kv.Value.AsString()
	case attribute.STRINGSLICE:
		s.attrs[k] = kv.Value.AsStringSlice()
	}
}

func (s *bufferedOTelSpan) TracerProvider() oteltrace.TracerProvider {
	return &contextTracerProvider{}
}

// NewSpanRecord constructs a normalized SpanRecord from start and end timestamps.
func NewSpanRecord(
	traceID, spanID, parentSpanID, name, kind, boundary, service string,
	start, end time.Time,
	status, statusMsg string,
	attrs map[string]any,
) SpanRecord {
	if start.IsZero() {
		start = time.Now().UTC()
	}
	if end.IsZero() || end.Before(start) {
		end = start.Add(10 * time.Microsecond)
	}
	durMs := float64(end.Sub(start).Microseconds()) / 1000.0
	if durMs <= 0 {
		durMs = 0.01
	}
	if kind == "" {
		kind = "INTERNAL"
	}
	if status == "" {
		status = "OK"
	}
	return SpanRecord{
		TraceID:       traceID,
		SpanID:        spanID,
		ParentSpanID:  parentSpanID,
		Name:          name,
		Kind:          kind,
		Boundary:      boundary,
		Service:       service,
		StartTimeUnix: start.UTC().UnixNano(),
		EndTimeUnix:   end.UTC().UnixNano(),
		StartTime:     start.UTC().Format(time.RFC3339Nano),
		EndTime:       end.UTC().Format(time.RFC3339Nano),
		DurationMs:    math.Round(durMs*100) / 100,
		Status:        status,
		StatusMessage: statusMsg,
		Attributes:    attrs,
	}
}

// EncodeOTLPJSON serializes spans into a standard OTLP/HTTP JSON ExportTraceServiceRequest.
func EncodeOTLPJSON(serviceName string, spans []SpanRecord) ([]byte, error) {
	otlpSpans := make([]map[string]any, 0, len(spans))
	for _, sp := range spans {
		scrubbed, _ := ScrubSpan(sp)
		attrsList := make([]map[string]any, 0, len(scrubbed.Attributes)+2)
		if scrubbed.Boundary != "" {
			attrsList = append(attrsList, map[string]any{
				"key":   "substrate.boundary",
				"value": map[string]any{"stringValue": scrubbed.Boundary},
			})
		}
		for k, v := range scrubbed.Attributes {
			if av, ok := toOTLPAnyValue(v); ok {
				attrsList = append(attrsList, map[string]any{
					"key":   k,
					"value": av,
				})
			}
		}
		statusCode := 1 // STATUS_CODE_OK
		if strings.EqualFold(scrubbed.Status, "ERROR") {
			statusCode = 2 // STATUS_CODE_ERROR
		}
		item := map[string]any{
			"traceId":           scrubbed.TraceID,
			"spanId":            scrubbed.SpanID,
			"parentSpanId":      scrubbed.ParentSpanID,
			"name":              scrubbed.Name,
			"kind":              otlpSpanKindNumber(scrubbed.Kind),
			"startTimeUnixNano": strconv.FormatInt(scrubbed.StartTimeUnix, 10),
			"endTimeUnixNano":   strconv.FormatInt(scrubbed.EndTimeUnix, 10),
			"attributes":        attrsList,
			"status": map[string]any{
				"code":    statusCode,
				"message": scrubbed.StatusMessage,
			},
		}
		otlpSpans = append(otlpSpans, item)
	}

	payload := map[string]any{
		"resourceSpans": []map[string]any{
			{
				"resource": map[string]any{
					"attributes": []map[string]any{
						{
							"key":   "service.name",
							"value": map[string]any{"stringValue": serviceName},
						},
					},
				},
				"scopeSpans": []map[string]any{
					{
						"scope": map[string]any{
							"name": "github.com/gke-demos/substrate-agent-platform/telemetry",
						},
						"spans": otlpSpans,
					},
				},
			},
		},
	}
	return json.Marshal(payload)
}

// PostOTLPSpans exports scrubbed spans keylessly from an Incident Actor to
// POST /otlp/v1/traces on brokerURL using the upstream OpenTelemetry OTLP/HTTP exporter.
func PostOTLPSpans(ctx context.Context, hc *http.Client, brokerURL, serviceName string, spans []SpanRecord) error {
	base := strings.TrimRight(strings.TrimSpace(brokerURL), "/")
	if base == "" || len(spans) == 0 {
		return nil
	}
	roSpans := spanRecordsToReadOnlySpans(serviceName, spans)
	if len(roSpans) == 0 {
		return nil
	}
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	postCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(base + "/otlp/v1/traces"),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}),
		otlptracehttp.WithHTTPClient(hc),
	}
	if !strings.HasPrefix(strings.ToLower(base), "https://") {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	exp, err := otlptracehttp.New(postCtx, opts...)
	if err != nil {
		return err
	}
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutCancel()
		_ = exp.Shutdown(shutCtx)
	}()
	return exp.ExportSpans(postCtx, roSpans)
}

func spanRecordsToReadOnlySpans(serviceName string, spans []SpanRecord) []tracesdk.ReadOnlySpan {
	svc := strings.TrimSpace(serviceName)
	if svc == "" {
		svc = "sre-agent"
	}
	res := resource.NewSchemaless(attribute.String("service.name", svc))
	scope := instrumentation.Scope{
		Name: "github.com/gke-demos/substrate-agent-platform/telemetry",
	}

	stubs := make(tracetest.SpanStubs, 0, len(spans))
	for _, sp := range spans {
		scrubbed, _ := ScrubSpan(sp)
		otelSC, ok := toOTelSpanContext(SpanContext{
			TraceID: scrubbed.TraceID,
			SpanID:  scrubbed.SpanID,
			Flags:   "01",
		}, false)
		if !ok {
			continue
		}
		var parentSC oteltrace.SpanContext
		if scrubbed.ParentSpanID != "" && scrubbed.ParentSpanID != zeroSpanID {
			if psc, pok := toOTelSpanContext(SpanContext{
				TraceID: scrubbed.TraceID,
				SpanID:  scrubbed.ParentSpanID,
				Flags:   "01",
			}, true); pok {
				parentSC = psc
			}
		}

		attrs := make([]attribute.KeyValue, 0, len(scrubbed.Attributes)+1)
		if scrubbed.Boundary != "" {
			attrs = append(attrs, attribute.String("substrate.boundary", scrubbed.Boundary))
		}
		for k, v := range scrubbed.Attributes {
			if kv, kvOK := toOTelAttribute(k, v); kvOK {
				attrs = append(attrs, kv)
			}
		}

		st := tracesdk.Status{Code: codes.Ok}
		if strings.EqualFold(scrubbed.Status, "ERROR") {
			st = tracesdk.Status{Code: codes.Error, Description: scrubbed.StatusMessage}
		}

		startT := time.Unix(0, scrubbed.StartTimeUnix).UTC()
		endT := time.Unix(0, scrubbed.EndTimeUnix).UTC()
		if scrubbed.StartTimeUnix <= 0 {
			startT = time.Now().UTC()
			endT = startT.Add(10 * time.Microsecond)
		}

		stubs = append(stubs, tracetest.SpanStub{
			Name:                 scrubbed.Name,
			SpanContext:          otelSC,
			Parent:               parentSC,
			SpanKind:             parseSpanKindEnum(scrubbed.Kind),
			StartTime:            startT,
			EndTime:              endT,
			Attributes:           attrs,
			Status:               st,
			Resource:             res,
			InstrumentationScope: scope,
		})
	}
	return stubs.Snapshots()
}

func toOTelAttribute(k string, v any) (attribute.KeyValue, bool) {
	switch val := v.(type) {
	case string:
		return attribute.String(k, val), true
	case bool:
		return attribute.Bool(k, val), true
	case int:
		return attribute.Int(k, val), true
	case int64:
		return attribute.Int64(k, val), true
	case int32:
		return attribute.Int64(k, int64(val)), true
	case float64:
		return attribute.Float64(k, val), true
	case []string:
		return attribute.StringSlice(k, val), true
	default:
		return attribute.KeyValue{}, false
	}
}

func parseSpanKindEnum(kind string) oteltrace.SpanKind {
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "SERVER":
		return oteltrace.SpanKindServer
	case "CLIENT":
		return oteltrace.SpanKindClient
	case "PRODUCER":
		return oteltrace.SpanKindProducer
	case "CONSUMER":
		return oteltrace.SpanKindConsumer
	default:
		return oteltrace.SpanKindInternal
	}
}

// DecodeOTLPRequest decodes either an OTLP/HTTP protobuf ExportTraceServiceRequest
// (sent by upstream otlptracehttp) or an OTLP/HTTP JSON trace batch, optionally
// decompressing gzip payloads up to maxBytes. It reports whether the wire payload
// was protobuf so the HTTP handler can serialize the matching response format.
func DecodeOTLPRequest(raw []byte, contentType, contentEncoding string, maxBytes int64) ([]SpanRecord, bool, error) {
	body := raw
	if strings.EqualFold(strings.TrimSpace(contentEncoding), "gzip") {
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, false, fmt.Errorf("invalid gzip OTLP payload: %w", err)
		}
		limit := maxBytes
		if limit <= 0 {
			limit = 4 << 20
		}
		decompressed, readErr := io.ReadAll(io.LimitReader(gz, limit+1))
		_ = gz.Close()
		if readErr != nil {
			return nil, false, fmt.Errorf("decompress OTLP payload: %w", readErr)
		}
		if int64(len(decompressed)) > limit {
			return nil, false, fmt.Errorf("decompressed OTLP payload exceeds %d byte limit", limit)
		}
		body = decompressed
	}

	ct := strings.ToLower(strings.TrimSpace(contentType))
	trimmed := bytes.TrimSpace(body)
	if strings.HasPrefix(ct, "application/x-protobuf") || (len(trimmed) > 0 && trimmed[0] != '{') {
		spans, err := DecodeOTLPProto(body)
		return spans, true, err
	}
	spans, err := DecodeOTLPJSON(body)
	return spans, false, err
}

// DecodeOTLPProto parses an OTLP/HTTP binary protobuf ExportTraceServiceRequest into SpanRecords.
func DecodeOTLPProto(raw []byte) ([]SpanRecord, error) {
	var req coltracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	var out []SpanRecord
	for _, rs := range req.GetResourceSpans() {
		resAttrs := parseOTLPProtoAttributes(rs.GetResource().GetAttributes())
		serviceName, _ := resAttrs["service.name"].(string)
		if serviceName == "" {
			serviceName = "sre-agent"
		}
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				if len(sp.GetTraceId()) != 16 || len(sp.GetSpanId()) != 8 || isAllZero(sp.GetTraceId()) || isAllZero(sp.GetSpanId()) {
					continue
				}
				tid := hex.EncodeToString(sp.GetTraceId())
				sid := hex.EncodeToString(sp.GetSpanId())
				var pid string
				if len(sp.GetParentSpanId()) == 8 && !isAllZero(sp.GetParentSpanId()) {
					pid = hex.EncodeToString(sp.GetParentSpanId())
				}

				startNano := int64(sp.GetStartTimeUnixNano())
				endNano := int64(sp.GetEndTimeUnixNano())
				startT := time.Unix(0, startNano).UTC()
				endT := time.Unix(0, endNano).UTC()
				if startNano <= 0 {
					startT = time.Now().UTC()
					endT = startT.Add(10 * time.Microsecond)
				}

				attrs := parseOTLPProtoAttributes(sp.GetAttributes())
				boundary, _ := attrs["substrate.boundary"].(string)
				delete(attrs, "substrate.boundary")
				if boundary == "" {
					boundary = "Incident Actor"
				}

				name := normalizeIncomingSpanName(sp.GetName(), attrs)
				status := "OK"
				if sp.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR {
					status = "ERROR"
				}
				rec := NewSpanRecord(
					tid,
					sid,
					pid,
					name,
					parseOTLPKind(float64(sp.GetKind())),
					boundary,
					serviceName,
					startT,
					endT,
					status,
					sp.GetStatus().GetMessage(),
					attrs,
				)
				out = append(out, rec)
			}
		}
	}
	return out, nil
}

func parseOTLPProtoAttributes(list []*commonpb.KeyValue) map[string]any {
	out := make(map[string]any, len(list))
	for _, item := range list {
		k := strings.TrimSpace(item.GetKey())
		val := item.GetValue()
		if k == "" || val == nil {
			continue
		}
		switch v := val.GetValue().(type) {
		case *commonpb.AnyValue_StringValue:
			out[k] = v.StringValue
		case *commonpb.AnyValue_BoolValue:
			out[k] = v.BoolValue
		case *commonpb.AnyValue_IntValue:
			out[k] = v.IntValue
		case *commonpb.AnyValue_DoubleValue:
			out[k] = v.DoubleValue
		case *commonpb.AnyValue_ArrayValue:
			if arr := v.ArrayValue.GetValues(); len(arr) > 0 {
				strs := make([]string, 0, len(arr))
				allStr := true
				for _, elem := range arr {
					sv, ok := elem.GetValue().(*commonpb.AnyValue_StringValue)
					if !ok {
						allStr = false
						break
					}
					strs = append(strs, sv.StringValue)
				}
				if allStr {
					out[k] = strs
					continue
				}
			}
			out[k] = v
		default:
			out[k] = val
		}
	}
	return out
}

// DecodeOTLPJSON parses an OTLP/HTTP JSON ExportTraceServiceRequest into SpanRecords.
func DecodeOTLPJSON(raw []byte) ([]SpanRecord, error) {
	var envelope struct {
		ResourceSpans []struct {
			Resource struct {
				Attributes []otlpKeyValue `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					TraceID           string          `json:"traceId"`
					SpanID            string          `json:"spanId"`
					ParentSpanID      string          `json:"parentSpanId"`
					Name              string          `json:"name"`
					Kind              any             `json:"kind"`
					StartTimeUnixNano json.RawMessage `json:"startTimeUnixNano"`
					EndTimeUnixNano   json.RawMessage `json:"endTimeUnixNano"`
					Attributes        []otlpKeyValue  `json:"attributes"`
					Status            struct {
						Code    any    `json:"code"`
						Message string `json:"message"`
					} `json:"status"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
		Spans []SpanRecord `json:"spans"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.ResourceSpans) == 0 && len(envelope.Spans) > 0 {
		return envelope.Spans, nil
	}

	var out []SpanRecord
	for _, rs := range envelope.ResourceSpans {
		resAttrs := parseOTLPAttributes(rs.Resource.Attributes)
		serviceName, _ := resAttrs["service.name"].(string)
		if serviceName == "" {
			serviceName = "sre-agent"
		}
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				tid := normalizeOTLPID(sp.TraceID, 16)
				sid := normalizeOTLPID(sp.SpanID, 8)
				if tid == "" || sid == "" {
					continue
				}
				pid := normalizeOTLPID(sp.ParentSpanID, 8)
				if pid == zeroSpanID {
					pid = ""
				}
				startNano := parseUnixNano(sp.StartTimeUnixNano)
				endNano := parseUnixNano(sp.EndTimeUnixNano)
				startT := time.Unix(0, startNano).UTC()
				endT := time.Unix(0, endNano).UTC()
				if startNano <= 0 {
					startT = time.Now().UTC()
					endT = startT.Add(10 * time.Microsecond)
				}

				attrs := parseOTLPAttributes(sp.Attributes)
				boundary, _ := attrs["substrate.boundary"].(string)
				delete(attrs, "substrate.boundary")
				if boundary == "" {
					boundary = "Incident Actor"
				}

				name := normalizeIncomingSpanName(sp.Name, attrs)
				status := parseOTLPStatus(sp.Status.Code)
				rec := NewSpanRecord(
					tid,
					sid,
					pid,
					name,
					parseOTLPKind(sp.Kind),
					boundary,
					serviceName,
					startT,
					endT,
					status,
					sp.Status.Message,
					attrs,
				)
				out = append(out, rec)
			}
		}
	}
	return out, nil
}

func normalizeIncomingSpanName(rawName string, attrs map[string]any) string {
	trimmed := strings.TrimSpace(rawName)
	switch {
	case strings.HasPrefix(trimmed, "invoke_agent "):
		attrs["gen_ai.operation.name"] = "invoke_agent"
		return "invoke_agent"
	case strings.HasPrefix(trimmed, "execute_tool "):
		if toolSuffix := strings.TrimSpace(strings.TrimPrefix(trimmed, "execute_tool")); toolSuffix != "" && emptyAttr(attrs, "gen_ai.tool.name") {
			attrs["gen_ai.tool.name"] = toolSuffix
		}
		attrs["gen_ai.operation.name"] = "execute_tool"
		return "execute_tool"
	default:
		return trimmed
	}
}

type otlpKeyValue struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

func parseOTLPAttributes(list []otlpKeyValue) map[string]any {
	out := make(map[string]any, len(list))
	for _, item := range list {
		k := strings.TrimSpace(item.Key)
		if k == "" || item.Value == nil {
			continue
		}
		if s, ok := item.Value["stringValue"].(string); ok {
			out[k] = s
		} else if b, ok := item.Value["boolValue"].(bool); ok {
			out[k] = b
		} else if f, ok := item.Value["intValue"].(float64); ok {
			out[k] = int64(f)
		} else if sInt, ok := item.Value["intValue"].(string); ok {
			if parsed, err := strconv.ParseInt(sInt, 10, 64); err == nil {
				out[k] = parsed
			}
		} else if f, ok := item.Value["doubleValue"].(float64); ok {
			out[k] = f
		} else {
			// Preserve presence marker so ScrubSpan drops/counts arbitrary nested objects.
			out[k] = item.Value
		}
	}
	return out
}

func toOTLPAnyValue(v any) (map[string]any, bool) {
	switch val := v.(type) {
	case string:
		return map[string]any{"stringValue": val}, true
	case bool:
		return map[string]any{"boolValue": val}, true
	case int:
		return map[string]any{"intValue": strconv.Itoa(val)}, true
	case int64:
		return map[string]any{"intValue": strconv.FormatInt(val, 10)}, true
	case int32:
		return map[string]any{"intValue": strconv.FormatInt(int64(val), 10)}, true
	case float64:
		return map[string]any{"doubleValue": val}, true
	default:
		return nil, false
	}
}

func otlpSpanKindNumber(kind string) int {
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "SERVER":
		return 2
	case "CLIENT":
		return 3
	case "PRODUCER":
		return 4
	case "CONSUMER":
		return 5
	default:
		return 1
	}
}

func parseOTLPKind(raw any) string {
	switch v := raw.(type) {
	case float64:
		switch int(v) {
		case 2:
			return "SERVER"
		case 3:
			return "CLIENT"
		case 4:
			return "PRODUCER"
		case 5:
			return "CONSUMER"
		default:
			return "INTERNAL"
		}
	case string:
		u := strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(v), "SPAN_KIND_"))
		switch u {
		case "SERVER", "CLIENT", "PRODUCER", "CONSUMER":
			return u
		default:
			return "INTERNAL"
		}
	default:
		return "INTERNAL"
	}
}

func parseOTLPStatus(code any) string {
	switch v := code.(type) {
	case float64:
		if int(v) == 2 {
			return "ERROR"
		}
	case string:
		if strings.Contains(strings.ToUpper(v), "ERROR") || v == "2" {
			return "ERROR"
		}
	}
	return "OK"
}

func normalizeOTLPID(raw string, byteLen int) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if isLowerHex(lower, byteLen*2) {
		return lower
	}
	if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(decoded) == byteLen {
		return hex.EncodeToString(decoded)
	}
	return ""
}

func parseUnixNano(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return n
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	return 0
}
