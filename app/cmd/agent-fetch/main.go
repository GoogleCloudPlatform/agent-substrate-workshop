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

// Command agent-fetch is optional lab I's copy of the workshop agent with one extra route,
// GET /fetch?url=..., which calls an external HTTPS API with a placeholder Authorization header.
// Under the sdsmint egress gateway, a matching EgressPolicy replaceHeaders rule replaces the
// placeholder with the real credential from the provider.
package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/agent-substrate/workshop/app/internal/agent"
)

func main() {
	cfg := agent.Config{
		Greeting:  "hello from my first actor",
		DataDir:   "/data",
		ModelURL:  os.Getenv("MODEL_URL"),
		Heartbeat: true,
	}
	srv, err := agent.New(cfg)
	if err != nil {
		log.Fatalf("agent: %v", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	mux := http.NewServeMux()
	mux.Handle("/", srv.Handler())
	mux.HandleFunc("GET /fetch", func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Query().Get("url")
		if !strings.HasPrefix(target, "https://") {
			http.Error(w, "url must be https://", http.StatusBadRequest)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
		if err != nil {
			http.Error(w, "fetch: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Placeholder only: the actor holds no secret, and the egress gateway's
		// replaceHeaders effect overwrites this with the real credential.
		req.Header.Set("Authorization", "Bearer placeholder")
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "fetch: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 64<<10))
	})

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":80"
	}
	log.Printf("agent-fetch listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
