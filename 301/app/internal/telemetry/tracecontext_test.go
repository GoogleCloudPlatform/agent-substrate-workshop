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
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gke-demos/substrate-agent-platform/app/internal/telemetry"
)

type trackingTransport struct {
	base       http.RoundTripper
	idleCloses atomic.Int32
}

func (t *trackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(req)
}

func (t *trackingTransport) CloseIdleConnections() {
	t.idleCloses.Add(1)
	if ic, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		ic.CloseIdleConnections()
	}
}

func TestWrapHTTPClientUpstreamResetRetries(t *testing.T) {
	const envoyResetBody = "upstream connect error or disconnect/reset before headers. reset reason: remote connection failure"
	const appDegradedBody = `{"status":"degraded","pool_exhausted":true}`

	tests := []struct {
		name           string
		method         string
		newBody        func() io.Reader
		firstAttempt   func(t *testing.T, w http.ResponseWriter)
		wantAttempts   int32
		wantIdleCloses int32
		wantStatus     int
		wantRespSubstr string
		wantReqBody    string
	}{{
		name:    "valid replayable POST with bytes.NewReader retries Envoy 503 reset",
		method:  http.MethodPost,
		newBody: func() io.Reader { return bytes.NewReader([]byte(`{"session_id":"inc-402"}`)) },
		firstAttempt: func(_ *testing.T, w http.ResponseWriter) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(envoyResetBody))
		},
		wantAttempts:   2,
		wantIdleCloses: 1,
		wantStatus:     http.StatusOK,
		wantRespSubstr: `"ok":true`,
		wantReqBody:    `{"session_id":"inc-402"}`,
	}, {
		name:    "valid replayable POST with bytes.NewReader retries abrupt connection EOF",
		method:  http.MethodPost,
		newBody: func() io.Reader { return bytes.NewReader([]byte(`{"ping":1}`)) },
		firstAttempt: func(t *testing.T, w http.ResponseWriter) {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter does not support Hijacker")
			}
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		},
		wantAttempts:   2,
		wantIdleCloses: 1,
		wantStatus:     http.StatusOK,
		wantRespSubstr: `"ok":true`,
		wantReqBody:    `{"ping":1}`,
	}, {
		name:   "non-replayable POST with nil GetBody is not retried",
		method: http.MethodPost,
		newBody: func() io.Reader {
			return io.NopCloser(strings.NewReader(`{"non_replayable":true}`))
		},
		firstAttempt: func(_ *testing.T, w http.ResponseWriter) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(envoyResetBody))
		},
		wantAttempts:   1,
		wantIdleCloses: 0,
		wantStatus:     http.StatusServiceUnavailable,
		wantRespSubstr: "upstream connect error",
	}, {
		name:    "application 503 without Envoy reset marker is not retried and preserves body",
		method:  http.MethodPost,
		newBody: func() io.Reader { return bytes.NewReader([]byte(`{}`)) },
		firstAttempt: func(_ *testing.T, w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(appDegradedBody))
		},
		wantAttempts:   1,
		wantIdleCloses: 0,
		wantStatus:     http.StatusServiceUnavailable,
		wantRespSubstr: appDegradedBody,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var attempts atomic.Int32
			var gotReqBody string
			var gotTraceparent string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := attempts.Add(1)
				raw, _ := io.ReadAll(r.Body)
				if n == 1 {
					tt.firstAttempt(t, w)
					return
				}
				gotReqBody = string(raw)
				gotTraceparent = r.Header.Get("traceparent")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			t.Cleanup(srv.Close)

			baseClient := srv.Client()
			tracked := &trackingTransport{base: baseClient.Transport}
			hc := telemetry.WrapHTTPClient(&http.Client{Transport: tracked}, 5*time.Second)

			ctx := telemetry.ContextWithSpanContext(context.Background(), telemetry.SpanContext{
				TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
				SpanID:  "00f067aa0ba902b7",
				Flags:   "01",
			})
			req, err := http.NewRequestWithContext(ctx, tt.method, srv.URL+"/agentengine/sessions:get", tt.newBody())
			if err != nil {
				t.Fatalf("NewRequestWithContext: %v", err)
			}

			resp, err := hc.Do(req)
			if err != nil {
				t.Fatalf("hc.Do: %v", err)
			}
			defer resp.Body.Close()
			respBytes, _ := io.ReadAll(resp.Body)

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d (%s), want %d", resp.StatusCode, string(respBytes), tt.wantStatus)
			}
			if attempts.Load() != tt.wantAttempts {
				t.Fatalf("attempts = %d, want %d", attempts.Load(), tt.wantAttempts)
			}
			if tracked.idleCloses.Load() != tt.wantIdleCloses {
				t.Fatalf("CloseIdleConnections calls = %d, want %d", tracked.idleCloses.Load(), tt.wantIdleCloses)
			}
			if !strings.Contains(string(respBytes), tt.wantRespSubstr) {
				t.Fatalf("response body = %q, want substring %q", string(respBytes), tt.wantRespSubstr)
			}
			if tt.wantReqBody != "" {
				if gotReqBody != tt.wantReqBody {
					t.Fatalf("replayed body = %q, want %q", gotReqBody, tt.wantReqBody)
				}
				if !strings.HasPrefix(gotTraceparent, "00-4bf92f3577b34da6a3ce929d0e0e4736-") {
					t.Fatalf("replayed traceparent = %q, want propagated trace ID", gotTraceparent)
				}
			}
		})
	}
}
