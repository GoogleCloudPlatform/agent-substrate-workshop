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

// Package ledgersvc implements the prod-sim Target Service (ledger-api),
// reproducing deterministic connection-pool exhaustion and leaked reconciliation
// workers under unpatched configuration and recovering when remediated.
package ledgersvc

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/netutil"
)

const (
	defaultCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'"
	maxBodyLen = 64 << 10
)

var (
	errPoolExhausted   = errors.New("ledger_db: connection pool exhausted")
	errChecksumInvalid = errors.New("ledger_db: transfer checksum mismatch")
)

// RuntimeConfig holds the active connection-pool parameters for ledger-api.
type RuntimeConfig struct {
	MaxOpenConns       int  `json:"max_open_conns"`
	IdleTimeoutSeconds int  `json:"idle_timeout_seconds"`
	AcquireTimeoutMs   int  `json:"acquire_timeout_ms"`
	LeakFixed          bool `json:"leak_fixed"`
}

// StatusSnapshot reports live health, pool saturation, and SLO compliance.
type StatusSnapshot struct {
	Service             string `json:"service"`
	Status              string `json:"status"`
	SLOMet              bool   `json:"slo_met"`
	PoolExhausted       bool   `json:"pool_exhausted"`
	MaxOpenConns        int    `json:"max_open_conns"`
	InUseConns          int    `json:"in_use_conns"`
	ActiveLeakedWorkers int    `json:"active_leaked_workers"`
	IdleTimeoutSeconds  int    `json:"idle_timeout_seconds"`
	AcquireTimeoutMs    int    `json:"acquire_timeout_ms"`
	LeakFixed           bool   `json:"leak_fixed"`
	PoolExhaustedTotal  uint64 `json:"pool_exhausted_total"`
	P99LatencyMs        int    `json:"p99_latency_ms"`
}

// Options configures a Server instance.
type Options struct {
	Config      RuntimeConfig
	ReloadToken string
	LogOutput   io.Writer
}

// Server hosts the HTTP handlers and connection pool state for ledger-api.
type Server struct {
	mu          sync.Mutex
	cfg         RuntimeConfig
	reloadToken string
	slots       chan struct{}
	reapCh      chan struct{}
	exhausted   bool
	leakedCount atomic.Int64
	exhaustedN  atomic.Uint64
	okTransfers atomic.Uint64
	errRequests atomic.Uint64
	logMu       sync.Mutex
	logOut      io.Writer
	limiter     *netutil.RateLimiter
	closed      bool
}

// NewServer constructs a Server initialized with opts.
func NewServer(opts Options) *Server {
	cfg := normalizeConfig(opts.Config)
	out := opts.LogOutput
	if out == nil {
		out = os.Stderr
	}
	reloadTok := strings.TrimSpace(opts.ReloadToken)
	if reloadTok == "" {
		reloadTok = strings.TrimSpace(os.Getenv("LEDGER_RELOAD_TOKEN"))
	}
	return &Server{
		cfg:         cfg,
		reloadToken: reloadTok,
		slots:       make(chan struct{}, cfg.MaxOpenConns),
		reapCh:      make(chan struct{}),
		logOut:      out,
		limiter:     netutil.NewRateLimiter(250, 500),
	}
}

func normalizeConfig(cfg RuntimeConfig) RuntimeConfig {
	if cfg.MaxOpenConns <= 0 {
		cfg.MaxOpenConns = 2
	}
	if cfg.MaxOpenConns > 256 {
		cfg.MaxOpenConns = 256
	}
	if cfg.IdleTimeoutSeconds < 0 {
		cfg.IdleTimeoutSeconds = 0
	}
	if cfg.AcquireTimeoutMs <= 0 {
		cfg.AcquireTimeoutMs = 150
	}
	return cfg
}

// Close releases any parked reconciliation worker goroutines.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.reapCh)
}

// ApplyConfig updates the connection pool parameters and reaps parked workers.
func (s *Server) ApplyConfig(next RuntimeConfig) StatusSnapshot {
	cfg := normalizeConfig(next)
	s.mu.Lock()
	if !s.closed {
		close(s.reapCh)
		s.reapCh = make(chan struct{})
	}
	s.cfg = cfg
	s.slots = make(chan struct{}, cfg.MaxOpenConns)
	s.exhausted = false
	s.mu.Unlock()

	// Yield briefly so parked worker goroutines decrement leakedCount on reapCh close.
	deadline := time.Now().Add(100 * time.Millisecond)
	for s.leakedCount.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snap := s.Snapshot()
	s.writeLog("INFO", "config_reloaded", "reloaded ledger_db connection pool configuration", http.StatusOK, snap)
	return snap
}

// Snapshot returns a point-in-time view of pool saturation and SLO status.
func (s *Server) Snapshot() StatusSnapshot {
	s.mu.Lock()
	cfg := s.cfg
	inUse := len(s.slots)
	exhausted := s.exhausted || inUse >= cfg.MaxOpenConns
	s.mu.Unlock()

	leaked := int(s.leakedCount.Load())
	sloMet := !exhausted && leaked == 0
	status := "healthy"
	p99 := 18
	if !sloMet {
		status = "degraded"
		p99 = cfg.AcquireTimeoutMs + 420
	}
	return StatusSnapshot{
		Service:             "ledger-api",
		Status:              status,
		SLOMet:              sloMet,
		PoolExhausted:       exhausted,
		MaxOpenConns:        cfg.MaxOpenConns,
		InUseConns:          inUse,
		ActiveLeakedWorkers: leaked,
		IdleTimeoutSeconds:  cfg.IdleTimeoutSeconds,
		AcquireTimeoutMs:    cfg.AcquireTimeoutMs,
		LeakFixed:           cfg.LeakFixed,
		PoolExhaustedTotal:  s.exhaustedN.Load(),
		P99LatencyMs:        p99,
	}
}

// Handler returns the secured HTTP mux for ledger-api.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", s.handleLivez)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/transfer", s.handleTransfer)
	mux.HandleFunc("/api/config/reload", s.handleReload)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w, strings.HasPrefix(r.URL.Path, "/api/"))
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, HEAD, POST")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if !s.limiter.Allow(netutil.ClientIP(r.RemoteAddr)) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) handleLivez(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	snap := s.Snapshot()
	code := http.StatusOK
	if !snap.SLOMet {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, snap)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, s.Snapshot())
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	snap := s.Snapshot()
	healthyVal := 0
	if snap.SLOMet {
		healthyVal = 1
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP ledger_pool_max_open_conns Maximum open connections configured in ledger_db pool.\n")
	fmt.Fprintf(w, "# TYPE ledger_pool_max_open_conns gauge\n")
	fmt.Fprintf(w, "ledger_pool_max_open_conns %d\n", snap.MaxOpenConns)
	fmt.Fprintf(w, "# HELP ledger_pool_in_use_conns Currently held connections in ledger_db pool.\n")
	fmt.Fprintf(w, "# TYPE ledger_pool_in_use_conns gauge\n")
	fmt.Fprintf(w, "ledger_pool_in_use_conns %d\n", snap.InUseConns)
	fmt.Fprintf(w, "# HELP ledger_active_leaked_workers Reconciliation workers holding unclosed row cursors.\n")
	fmt.Fprintf(w, "# TYPE ledger_active_leaked_workers gauge\n")
	fmt.Fprintf(w, "ledger_active_leaked_workers %d\n", snap.ActiveLeakedWorkers)
	fmt.Fprintf(w, "# HELP ledger_pool_exhausted_total Total requests rejected due to connection pool exhaustion.\n")
	fmt.Fprintf(w, "# TYPE ledger_pool_exhausted_total counter\n")
	fmt.Fprintf(w, "ledger_pool_exhausted_total %d\n", snap.PoolExhaustedTotal)
	fmt.Fprintf(w, "# HELP ledger_slo_healthy 1 when pool and latency SLOs are met, 0 when degraded.\n")
	fmt.Fprintf(w, "# TYPE ledger_slo_healthy gauge\n")
	fmt.Fprintf(w, "ledger_slo_healthy %d\n", healthyVal)
	fmt.Fprintf(w, "# HELP ledger_transfers_ok_total Successful ledger transfers.\n")
	fmt.Fprintf(w, "# TYPE ledger_transfers_ok_total counter\n")
	fmt.Fprintf(w, "ledger_transfers_ok_total %d\n", s.okTransfers.Load())
}

type transferRequest struct {
	TxID        string `json:"tx_id"`
	AmountCents int64  `json:"amount_cents"`
}

func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req transferRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyLen))
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid transfer request JSON"})
		return
	}
	req.TxID = strings.TrimSpace(req.TxID)
	if req.TxID == "" || len(req.TxID) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tx_id must be 1..64 characters"})
		return
	}

	err := s.ExecuteTransfer(r.Context(), req.TxID)
	if errors.Is(err, errPoolExhausted) {
		snap := s.Snapshot()
		s.writeLog("ERROR", "pool_exhausted", "connection pool exhausted acquiring ledger_db slot", http.StatusServiceUnavailable, snap)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":                 "pool_exhausted",
			"message":               err.Error(),
			"active_leaked_workers": snap.ActiveLeakedWorkers,
			"max_open_conns":        snap.MaxOpenConns,
		})
		return
	}
	if errors.Is(err, errChecksumInvalid) {
		snap := s.Snapshot()
		s.writeLog("WARNING", "checksum_mismatch", "reconciliation checksum mismatch on transfer batch", http.StatusUnprocessableEntity, snap)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":                 "checksum_mismatch",
			"active_leaked_workers": snap.ActiveLeakedWorkers,
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "transfer interrupted"})
		return
	}
	s.okTransfers.Add(1)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "committed",
		"tx_id":  req.TxID,
	})
}

// ExecuteTransfer runs a single transfer reconciliation against the pool.
func (s *Server) ExecuteTransfer(ctx context.Context, txID string) error {
	s.mu.Lock()
	cfg := s.cfg
	slots := s.slots
	reapCh := s.reapCh
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("server closed")
	}

	timer := time.NewTimer(time.Duration(cfg.AcquireTimeoutMs) * time.Millisecond)
	defer timer.Stop()

	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		s.exhaustedN.Add(1)
		s.errRequests.Add(1)
		s.mu.Lock()
		if s.slots == slots {
			s.exhausted = true
		}
		s.mu.Unlock()
		return errPoolExhausted
	}

	release := func() { <-slots }
	if cfg.LeakFixed {
		defer release()
	}

	if strings.HasPrefix(txID, "bad-") {
		s.errRequests.Add(1)
		if !cfg.LeakFixed {
			s.parkLeakedWorker(slots, reapCh, cfg.IdleTimeoutSeconds)
		}
		return errChecksumInvalid
	}

	if !cfg.LeakFixed {
		release()
	}
	return nil
}

func (s *Server) parkLeakedWorker(slots chan struct{}, reapCh chan struct{}, idleTimeoutSec int) {
	s.leakedCount.Add(1)
	go func() {
		defer s.leakedCount.Add(-1)
		if idleTimeoutSec > 0 {
			timer := time.NewTimer(time.Duration(idleTimeoutSec) * time.Second)
			defer timer.Stop()
			select {
			case <-reapCh:
				return
			case <-timer.C:
				<-slots
				return
			}
		}
		<-reapCh
	}()
}

// TODO(security): In production, restrict /api/config/reload to mTLS or NetworkPolicy-isolated controller callers.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.reloadToken != "" {
		got := strings.TrimSpace(r.Header.Get("X-Ledger-Reload-Token"))
		if len(got) != len(s.reloadToken) || subtle.ConstantTimeCompare([]byte(got), []byte(s.reloadToken)) != 1 {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing or invalid X-Ledger-Reload-Token"})
			return
		}
	}
	var next RuntimeConfig
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyLen))
	if err := dec.Decode(&next); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid config JSON"})
		return
	}
	snap := s.ApplyConfig(next)
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) writeLog(severity, event, msg string, httpStatus int, snap StatusSnapshot) {
	rec := map[string]any{
		"severity":              severity,
		"service":               "ledger-api",
		"event":                 event,
		"message":               msg,
		"httpStatus":            httpStatus,
		"max_open_conns":        snap.MaxOpenConns,
		"in_use_conns":          snap.InUseConns,
		"active_leaked_workers": snap.ActiveLeakedWorkers,
		"idle_timeout_seconds":  snap.IdleTimeoutSeconds,
		"timestamp":             time.Now().UTC().Format(time.RFC3339Nano),
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	_, _ = s.logOut.Write(append(b, '\n'))
}

func setSecurityHeaders(w http.ResponseWriter, noStore bool) {
	h := w.Header()
	h.Set("Content-Security-Policy", defaultCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if noStore {
		h.Set("Cache-Control", "no-store")
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
