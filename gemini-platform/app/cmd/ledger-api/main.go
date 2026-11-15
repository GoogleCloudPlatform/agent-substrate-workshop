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

// Command ledger-api runs the prod-sim Target Service.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/ledgersvc"
)

func main() {
	cfg := ledgersvc.RuntimeConfig{
		MaxOpenConns:       envInt("MAX_OPEN_CONNS", 2),
		IdleTimeoutSeconds: envInt("IDLE_TIMEOUT_SECONDS", 0),
		AcquireTimeoutMs:   envInt("ACQUIRE_TIMEOUT_MS", 150),
		LeakFixed:          strings.EqualFold(os.Getenv("LEAK_FIXED"), "true"),
	}
	svc := ledgersvc.NewServer(ledgersvc.Options{Config: cfg})
	defer svc.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if strings.EqualFold(os.Getenv("SYNTHETIC_LOAD"), "true") {
		go runSyntheticTraffic(ctx, svc)
	}
	if syncURL := strings.TrimSpace(os.Getenv("OPS_HUB_CONFIG_URL")); syncURL != "" {
		go pollOpsHubConfig(ctx, svc, syncURL)
	}

	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "Error: ledger-api listen failed: %v\n", err)
		os.Exit(1)
	}
}

func runSyntheticTraffic(ctx context.Context, svc *ledgersvc.Server) {
	// Prime two checksum-mismatch reconciliation batches at startup so live
	// prod-sim immediately exhibits connection-pool exhaustion until patched.
	for i := 1; i <= 2; i++ {
		_ = svc.ExecuteTransfer(ctx, fmt.Sprintf("bad-boot-%02d", i))
	}
	_ = svc.ExecuteTransfer(ctx, "tx-boot-probe")

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	seq := 1
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			txID := fmt.Sprintf("tx-synth-%04d", seq)
			if seq%4 == 0 {
				txID = fmt.Sprintf("bad-synth-%04d", seq)
			}
			seq++
			reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := svc.ExecuteTransfer(reqCtx, txID)
			cancel()
			if err != nil && strings.HasPrefix(txID, "tx-") {
				// Emit structured ERROR log through the HTTP handler path equivalent.
				snap := svc.Snapshot()
				if snap.PoolExhausted {
					b, _ := json.Marshal(map[string]any{
						"severity":              "ERROR",
						"service":               "ledger-api",
						"event":                 "pool_exhausted",
						"message":               "connection pool exhausted acquiring ledger_db slot",
						"httpStatus":            http.StatusServiceUnavailable,
						"max_open_conns":        snap.MaxOpenConns,
						"in_use_conns":          snap.InUseConns,
						"active_leaked_workers": snap.ActiveLeakedWorkers,
						"idle_timeout_seconds":  snap.IdleTimeoutSeconds,
						"timestamp":             time.Now().UTC().Format(time.RFC3339Nano),
					})
					_, _ = os.Stderr.Write(append(b, '\n'))
				}
			}
		}
	}
}

func pollOpsHubConfig(ctx context.Context, svc *ledgersvc.Server, syncURL string) {
	client := &http.Client{Timeout: 3 * time.Second}
	var last ledgersvc.RuntimeConfig
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, syncURL, nil)
			if err != nil {
				continue
			}
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			var remote ledgersvc.RuntimeConfig
			err = json.NewDecoder(resp.Body).Decode(&remote)
			resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK || remote.MaxOpenConns <= 0 {
				continue
			}
			if remote != last {
				last = remote
				cur := svc.Snapshot()
				if cur.MaxOpenConns != remote.MaxOpenConns || cur.IdleTimeoutSeconds != remote.IdleTimeoutSeconds || cur.LeakFixed != remote.LeakFixed {
					svc.ApplyConfig(remote)
				}
			}
		}
	}
}

func envInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
