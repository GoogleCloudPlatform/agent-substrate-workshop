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

package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/telemetry"
)

func TestCloudTraceExportOmitsDefaultQuotaHeaderAndRecordsErrors(t *testing.T) {
	var force403 atomic.Bool
	var gotQuotaHeader atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuotaHeader.Store(r.Header.Get("X-Goog-User-Project"))
		if r.Header.Get("X-Goog-User-Project") != "" || force403.Load() {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"USER_PROJECT_DENIED"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	metrics := telemetry.NewMetricsRegistry()
	store := telemetry.NewStore(context.Background(), telemetry.StoreConfig{
		ProjectID:   "demo-proj",
		Region:      "us-central1",
		Atespace:    "workshop-301",
		MockLLM:     false,
		UpstreamURL: srv.URL,
		Metrics:     metrics,
	})

	now := time.Now().UTC().UnixNano()
	const traceOK = "4bf92f3577b34da6a3ce929d0e0e4736"
	store.RecordSpans(context.Background(), []telemetry.SpanRecord{{
		TraceID:       traceOK,
		SpanID:        "00f067aa0ba902b7",
		Name:          "invoke_gateway_turn",
		Kind:          "SERVER",
		Boundary:      "Governed Broker",
		Service:       "sre-gateway",
		StartTimeUnix: now,
		EndTimeUnix:   now + int64(15*time.Millisecond),
		Status:        "OK",
	}})

	if qh, _ := gotQuotaHeader.Load().(string); qh != "" {
		t.Fatalf("unexpected X-Goog-User-Project header %q on Workload Identity Cloud Trace export", qh)
	}
	wfOK, ok := store.GetTrace(traceOK)
	if !ok || !wfOK.ExportedToCloudTrace {
		t.Fatalf("GetTrace(%s) ExportedToCloudTrace = %t (ok=%t), want true", traceOK, wfOK.ExportedToCloudTrace, ok)
	}

	// Simulate an upstream 403 failure and verify ExportedToCloudTrace stays false while error counter increments.
	force403.Store(true)
	const traceFail = "11112222333344445555666677778888"
	store.RecordSpans(context.Background(), []telemetry.SpanRecord{{
		TraceID:       traceFail,
		SpanID:        "1122334455667788",
		Name:          "invoke_gateway_turn",
		Kind:          "SERVER",
		Boundary:      "Governed Broker",
		Service:       "sre-gateway",
		StartTimeUnix: now,
		EndTimeUnix:   now + int64(10*time.Millisecond),
		Status:        "OK",
	}})

	wfFail, ok := store.GetTrace(traceFail)
	if !ok || wfFail.ExportedToCloudTrace {
		t.Fatalf("GetTrace(%s) ExportedToCloudTrace = %t, want false on HTTP 403", traceFail, wfFail.ExportedToCloudTrace)
	}
	if store.ExportErrorCount() != 1 {
		t.Fatalf("ExportErrorCount() = %d, want 1", store.ExportErrorCount())
	}
	rendered := metrics.RenderText()
	if !strings.Contains(rendered, `sre_gateway_otel_spans_exported_total{destination="cloud_trace"} 1`) {
		t.Fatalf("metrics missing cloud_trace exported count:\n%s", rendered)
	}
	if !strings.Contains(rendered, `sre_gateway_otel_export_errors_total{destination="cloud_trace"} 1`) {
		t.Fatalf("metrics missing cloud_trace export error counter:\n%s", rendered)
	}
}
