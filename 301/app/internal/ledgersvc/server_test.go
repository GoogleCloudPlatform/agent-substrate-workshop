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

package ledgersvc_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gke-demos/substrate-agent-platform/app/internal/ledgersvc"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestLedgerServiceDegradationAndRecovery(t *testing.T) {
	logs := &syncBuffer{}
	svc := ledgersvc.NewServer(ledgersvc.Options{
		Config: ledgersvc.RuntimeConfig{
			MaxOpenConns:       2,
			IdleTimeoutSeconds: 0,
			AcquireTimeoutMs:   60,
			LeakFixed:          false,
		},
		LogOutput: logs,
	})
	t.Cleanup(svc.Close)

	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)
	if !strings.HasPrefix(ts.URL, "http://127.0.0.1:") {
		t.Fatalf("test server bound to %q, want 127.0.0.1", ts.URL)
	}

	// Verify HTTP method allowlist and security headers.
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/status", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /api/status: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /api/status = %d, want %d", resp.StatusCode, http.StatusMethodNotAllowed)
	}
	for hdr, wantSub := range map[string]string{
		"Content-Security-Policy": "default-src 'self'",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Permissions-Policy":      "camera=()",
	} {
		if got := resp.Header.Get(hdr); !strings.Contains(got, wantSub) {
			t.Errorf("header %s = %q, want substring %q", hdr, got, wantSub)
		}
	}

	// Trigger two checksum-mismatch transfers to leak both pool slots.
	for _, txID := range []string{"bad-001", "bad-002"} {
		code, _ := postJSON(t, ts.URL+"/api/transfer", map[string]any{"tx_id": txID, "amount_cents": 1500})
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("POST /api/transfer (%s) = %d, want 422", txID, code)
		}
	}

	// Next valid transfer must fail with 503 pool_exhausted and emit structured Cloud Logging ERROR JSON.
	code, body := postJSON(t, ts.URL+"/api/transfer", map[string]any{"tx_id": "tx-1001", "amount_cents": 4200})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/transfer (tx-1001) = %d (%s), want 503", code, body)
	}
	if !strings.Contains(body, "pool_exhausted") {
		t.Errorf("503 body = %q, want pool_exhausted", body)
	}

	// Check structured JSON Cloud Logging severity ERROR record.
	foundErrorLog := false
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not valid JSON: %q: %v", line, err)
		}
		if rec["severity"] == "ERROR" && rec["event"] == "pool_exhausted" {
			foundErrorLog = true
			if got, _ := rec["active_leaked_workers"].(float64); got != 2 {
				t.Errorf("log active_leaked_workers = %v, want 2", rec["active_leaked_workers"])
			}
		}
	}
	if !foundErrorLog {
		t.Fatalf("expected structured severity=ERROR event=pool_exhausted log, got:\n%s", logs.String())
	}

	// Liveness stays 200 so Kubernetes Service endpoints stay active while /healthz reports 503 degraded.
	if code, _ := getBody(t, ts.URL+"/livez"); code != http.StatusOK {
		t.Fatalf("GET /livez = %d, want 200", code)
	}
	if code, _ := getBody(t, ts.URL+"/healthz"); code != http.StatusServiceUnavailable {
		t.Fatalf("GET /healthz while exhausted = %d, want 503", code)
	}

	// /api/status reports pool exhaustion and Cache-Control: no-store; legacy /api/Status is absent.
	if legacyCode, _ := getBody(t, ts.URL+"/api/Status"); legacyCode != http.StatusNotFound {
		t.Fatalf("GET /api/Status = %d, want 404", legacyCode)
	}
	stResp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	raw, _ := io.ReadAll(stResp.Body)
	stResp.Body.Close()
	if got := stResp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("/api/status Cache-Control = %q, want no-store", got)
	}
	var st ledgersvc.StatusSnapshot
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("unmarshal /api/status: %v", err)
	}
	if !st.PoolExhausted || st.SLOMet || st.ActiveLeakedWorkers != 2 {
		t.Fatalf("/api/status snapshot = %+v, want PoolExhausted=true SLOMet=false ActiveLeakedWorkers=2", st)
	}

	_, metricsBody := getBody(t, ts.URL+"/metrics")
	for _, wantMetric := range []string{
		"ledger_pool_max_open_conns 2",
		"ledger_active_leaked_workers 2",
		"ledger_slo_healthy 0",
	} {
		if !strings.Contains(metricsBody, wantMetric) {
			t.Errorf("/metrics missing %q in:\n%s", wantMetric, metricsBody)
		}
	}

	// Apply remediated configuration and verify clean recovery.
	reloadCode, reloadBody := postJSON(t, ts.URL+"/api/config/reload", ledgersvc.RuntimeConfig{
		MaxOpenConns:       32,
		IdleTimeoutSeconds: 30,
		AcquireTimeoutMs:   150,
		LeakFixed:          true,
	})
	if reloadCode != http.StatusOK {
		t.Fatalf("POST /api/config/reload = %d (%s), want 200", reloadCode, reloadBody)
	}

	// Bad checksum transfers no longer leak pool slots.
	for i := 0; i < 5; i++ {
		c, _ := postJSON(t, ts.URL+"/api/transfer", map[string]any{"tx_id": "bad-after-fix", "amount_cents": 100})
		if c != http.StatusUnprocessableEntity {
			t.Fatalf("POST /api/transfer bad-after-fix = %d, want 422", c)
		}
	}

	okCode, okBody := postJSON(t, ts.URL+"/api/transfer", map[string]any{"tx_id": "tx-2001", "amount_cents": 9900})
	if okCode != http.StatusOK {
		t.Fatalf("POST /api/transfer after fix = %d (%s), want 200", okCode, okBody)
	}
	if hCode, _ := getBody(t, ts.URL+"/healthz"); hCode != http.StatusOK {
		t.Fatalf("GET /healthz after fix = %d, want 200", hCode)
	}
	_, stRaw := getBody(t, ts.URL+"/api/status")
	var recovered ledgersvc.StatusSnapshot
	if err := json.Unmarshal([]byte(stRaw), &recovered); err != nil {
		t.Fatalf("unmarshal recovered status: %v", err)
	}
	if recovered.PoolExhausted || !recovered.SLOMet || recovered.ActiveLeakedWorkers != 0 || recovered.MaxOpenConns != 32 {
		t.Fatalf("recovered status = %+v, want healthy 32-conn pool with 0 leaked workers", recovered)
	}
	_, recoveredMetrics := getBody(t, ts.URL+"/metrics")
	if !strings.Contains(recoveredMetrics, "ledger_slo_healthy 1") {
		t.Errorf("recovered /metrics missing ledger_slo_healthy 1:\n%s", recoveredMetrics)
	}
}

func TestLedgerReloadTokenGuard(t *testing.T) {
	svc := ledgersvc.NewServer(ledgersvc.Options{
		Config:      ledgersvc.RuntimeConfig{MaxOpenConns: 2, AcquireTimeoutMs: 60},
		ReloadToken: "internal-reload-secret-99",
		LogOutput:   io.Discard,
	})
	t.Cleanup(svc.Close)
	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)

	deniedCode, _ := postJSON(t, ts.URL+"/api/config/reload", ledgersvc.RuntimeConfig{MaxOpenConns: 16, LeakFixed: true})
	if deniedCode != http.StatusForbidden {
		t.Fatalf("reload without X-Ledger-Reload-Token = %d, want 403", deniedCode)
	}

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/config/reload", strings.NewReader(`{"max_open_conns":16,"leak_fixed":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ledger-Reload-Token", "internal-reload-secret-99")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/config/reload with token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reload with valid X-Ledger-Reload-Token = %d, want 200", resp.StatusCode)
	}
}

func postJSON(t *testing.T, url string, payload any) (int, string) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}
