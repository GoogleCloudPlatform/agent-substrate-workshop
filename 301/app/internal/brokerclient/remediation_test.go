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

package brokerclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gke-demos/substrate-agent-platform/app/internal/opshub"
)

func TestApplyRemediationPRRepushesBranchAfterOpsHubRestart(t *testing.T) {
	ctx := context.Background()

	newOpsHub := func() *httptest.Server {
		t.Helper()
		srv, err := opshub.NewServer(opshub.Options{
			DataDir:      t.TempDir(),
			SimNamespace: "prod-sim",
		})
		if err != nil {
			t.Fatalf("opshub.NewServer: %v", err)
		}
		return httptest.NewServer(srv.Handler())
	}

	opsHub1 := newOpsHub()
	defer opsHub1.Close()
	var currentOpsHubURL atomic.Pointer[string]
	u1 := opsHub1.URL
	currentOpsHubURL.Store(&u1)

	brokerProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBase := *currentOpsHubURL.Load()
		var target string
		switch {
		case strings.HasPrefix(r.URL.Path, "/ops/"):
			target = upstreamBase + strings.TrimPrefix(r.URL.Path, "/ops")
		case r.URL.Path == "/mcp/ops-hub-mcp":
			target = upstreamBase + "/mcp"
		default:
			http.NotFound(w, r)
			return
		}
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		upReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		upReq.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(upReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(readAllOrEmpty(resp))
	}))
	defer brokerProxy.Close()

	wsDir := t.TempDir()
	repoDir := filepath.Join(wsDir, "ledger-api")
	if _, err := runRemediationGit(ctx, "git", wsDir, "clone", brokerProxy.URL+"/ops/git/ledger-api.git", repoDir); err != nil {
		t.Fatalf("git clone: %v", err)
	}
	if _, err := runRemediationGit(ctx, "git", repoDir, "checkout", "-b", "fix/inc-402-pool-leak"); err != nil {
		t.Fatalf("git checkout -b: %v", err)
	}
	poolYAML := "# Connection pool settings for ledger-api.\nmax_open_conns: 32\nidle_timeout_seconds: 30\nacquire_timeout_ms: 150\n"
	if err := os.WriteFile(filepath.Join(repoDir, "config", "pool.yaml"), []byte(poolYAML), 0o644); err != nil {
		t.Fatalf("write pool.yaml: %v", err)
	}
	rawStore, err := os.ReadFile(filepath.Join(repoDir, "pool", "store.go"))
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	patchedStore := strings.Replace(
		string(rawStore),
		"\tif err := rows.ValidateChecksum(); err != nil {",
		"\tdefer rows.Close()\n\tif err := rows.ValidateChecksum(); err != nil {",
		1,
	)
	if err := os.WriteFile(filepath.Join(repoDir, "pool", "store.go"), []byte(patchedStore), 0o644); err != nil {
		t.Fatalf("write store.go: %v", err)
	}
	if _, err := runRemediationGit(ctx, "git", repoDir, "add", "."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runRemediationGit(ctx, "git", repoDir, "commit", "-m", "fix pool"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	exec := &remediationExecutor{
		baseURL:      brokerProxy.URL,
		workspaceDir: wsDir,
		hc:           brokerProxy.Client(),
	}

	// Module 04 pushes fix/inc-402-pool-leak to opsHub1 before checkpointing RAM.
	pushRes, err := exec.pushBranch(ctx, "fix/inc-402-pool-leak")
	if err != nil || !pushRes.Pushed {
		t.Fatalf("initial pushBranch failed: res=%+v err=%v", pushRes, err)
	}

	// Module 05 rolls deploy/ops-hub with OPS_HUB_REQUIRE_3LO=true, resetting its ephemeral bare repo,
	// while inc-402 resumes from its SNAPSHOT_FIDELITY_MEMORY checkpoint.
	opsHub2 := newOpsHub()
	defer opsHub2.Close()
	u2 := opsHub2.URL
	currentOpsHubURL.Store(&u2)

	prRes, err := exec.applyRemediationPR(ctx, ApplyRemediationPRArgs{
		IncidentID: "INC-402",
		Branch:     "fix/inc-402-pool-leak",
		Title:      "Fix connection pool leak for INC-402",
	})
	if err != nil {
		t.Fatalf("applyRemediationPR after ops-hub restart failed: %v", err)
	}
	if !prRes.Merged || prRes.Status != "MERGED" {
		t.Fatalf("expected merged PR after ops-hub restart, got %+v", prRes)
	}
}

func readAllOrEmpty(resp *http.Response) []byte {
	b := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			b = append(b, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	return b
}
