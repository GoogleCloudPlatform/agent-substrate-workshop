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

// Command ops-hub runs the self-contained prod-sim Operations Hub.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gke-demos/substrate-agent-platform/app/internal/opshub"
)

func main() {
	clientSecret := strings.TrimSpace(os.Getenv("OPS_HUB_OAUTH_CLIENT_SECRET"))
	if secretFile := strings.TrimSpace(os.Getenv("OPS_HUB_OAUTH_CLIENT_SECRET_FILE")); clientSecret == "" && secretFile != "" {
		if raw, err := os.ReadFile(secretFile); err == nil {
			clientSecret = strings.TrimSpace(string(raw))
		}
	}
	if clientSecret == "" {
		fmt.Fprintln(os.Stderr, "Warning: OPS_HUB_OAUTH_CLIENT_SECRET not set; generating instance-isolated ephemeral secret")
	}

	srv, err := opshub.NewServer(opshub.Options{
		DataDir:           os.Getenv("DATA_DIR"),
		PublicBaseURL:     os.Getenv("PUBLIC_BASE_URL"),
		PublicGitURL:      os.Getenv("PUBLIC_GIT_URL"),
		SimNamespace:      os.Getenv("SIM_NAMESPACE"),
		LedgerAPIURL:      os.Getenv("LEDGER_API_URL"),
		Require3LO:        strings.EqualFold(os.Getenv("OPS_HUB_REQUIRE_3LO"), "true"),
		OAuthClientID:     os.Getenv("OPS_HUB_OAUTH_CLIENT_ID"),
		OAuthClientSecret: clientSecret,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: initialize ops-hub: %v\n", err)
		os.Exit(1)
	}

	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "Error: ops-hub listen failed: %v\n", err)
		os.Exit(1)
	}
}
