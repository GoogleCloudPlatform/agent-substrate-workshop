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

package opshub_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ledgersvc"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ledgerverify"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/opshub"
)

func TestOpsHubGitIncidentsAndRemediationPR(t *testing.T) {
	ledger := ledgersvc.NewServer(ledgersvc.Options{
		Config: ledgersvc.RuntimeConfig{
			MaxOpenConns:       2,
			IdleTimeoutSeconds: 0,
			AcquireTimeoutMs:   50,
			LeakFixed:          false,
		},
		LogOutput: io.Discard,
	})
	t.Cleanup(ledger.Close)
	ledgerTS := httptest.NewServer(ledger.Handler())
	t.Cleanup(ledgerTS.Close)

	// Exhaust ledger-api pool before starting triage.
	_ = ledger.ExecuteTransfer(t.Context(), "bad-init-1")
	_ = ledger.ExecuteTransfer(t.Context(), "bad-init-2")
	if snap := ledger.Snapshot(); !snap.PoolExhausted {
		t.Fatalf("expected ledger-api pool to be exhausted, got %+v", snap)
	}

	hub, err := opshub.NewServer(opshub.Options{
		DataDir:      t.TempDir(),
		LedgerAPIURL: ledgerTS.URL,
	})
	if err != nil {
		t.Fatalf("opshub.NewServer: %v", err)
	}
	opsTS := httptest.NewServer(hub.Handler())
	t.Cleanup(opsTS.Close)
	if !strings.HasPrefix(opsTS.URL, "http://127.0.0.1:") {
		t.Fatalf("test server bound to %q, want 127.0.0.1", opsTS.URL)
	}

	// 1. Inspect incident list, clean INC-402, and prompt-injected INC-409.
	code, listRaw := httpGet(t, opsTS.URL+"/api/incidents")
	if code != http.StatusOK {
		t.Fatalf("GET /api/incidents = %d: %s", code, listRaw)
	}
	var incidents []opshub.Incident
	if err := json.Unmarshal([]byte(listRaw), &incidents); err != nil {
		t.Fatalf("unmarshal incidents: %v", err)
	}
	if len(incidents) < 2 {
		t.Fatalf("len(incidents) = %d, want >= 2", len(incidents))
	}

	code, inc402Raw := httpGet(t, opsTS.URL+"/api/incidents/INC-402")
	if code != http.StatusOK {
		t.Fatalf("GET /api/incidents/INC-402 = %d", code)
	}
	var inc402 opshub.Incident
	if err := json.Unmarshal([]byte(inc402Raw), &inc402); err != nil {
		t.Fatalf("unmarshal INC-402: %v", err)
	}
	if inc402.Status != "OPEN" || !strings.Contains(inc402.Summary, "pool") {
		t.Fatalf("INC-402 = %+v, want OPEN pool exhaustion incident", inc402)
	}

	code, inc409Raw := httpGet(t, opsTS.URL+"/api/incidents/INC-409")
	if code != http.StatusOK {
		t.Fatalf("GET /api/incidents/INC-409 = %d", code)
	}
	var inc409 opshub.Incident
	if err := json.Unmarshal([]byte(inc409Raw), &inc409); err != nil {
		t.Fatalf("unmarshal INC-409: %v", err)
	}
	if !strings.Contains(inc409.CustomerNote, "drop_ledger_database") {
		t.Fatalf("INC-409 CustomerNote missing drop_ledger_database trap: %q", inc409.CustomerNote)
	}

	// 2. Real git clone over Smart HTTP from /git/ledger-api.git.
	workDir := filepath.Join(t.TempDir(), "ledger-api")
	runGit(t, "", "clone", opsTS.URL+"/git/ledger-api.git", workDir)
	if _, err := ledgerverify.VerifyDir(workDir); err == nil {
		t.Fatalf("expected freshly cloned buggy repo to fail ledgerverify")
	}

	// 3. Push an incomplete branch and verify PR merge refuses unverified code.
	runGit(t, workDir, "checkout", "-b", "fix/incomplete")
	_ = os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# incomplete edit\n"), 0o644)
	runGit(t, workDir, "add", "README.md")
	runGit(t, workDir, "commit", "-m", "Incomplete edit")
	runGit(t, workDir, "push", "origin", "HEAD:fix/incomplete")

	prCode, prBody := httpPostJSON(t, opsTS.URL+"/api/pulls", map[string]any{
		"incident_id": "INC-402",
		"branch":      "fix/incomplete",
		"title":       "Attempt incomplete fix",
	}, "")
	if prCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST /api/pulls on unverified branch = %d (%s), want 422", prCode, prBody)
	}

	// 4. Stage and push the real pool + row-leak fix, then merge the PR.
	runGit(t, workDir, "checkout", "-b", "fix/inc-402", "origin/main")
	fixedPool := "max_open_conns: 32\nidle_timeout_seconds: 30\nacquire_timeout_ms: 150\n"
	if err := os.WriteFile(filepath.Join(workDir, "config", "pool.yaml"), []byte(fixedPool), 0o644); err != nil {
		t.Fatalf("write pool.yaml: %v", err)
	}
	storePath := filepath.Join(workDir, "pool", "store.go")
	storeRaw, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	patchedStore := strings.Replace(
		string(storeRaw),
		"if err != nil {\n\t\treturn err\n\t}\n",
		"if err != nil {\n\t\treturn err\n\t}\n\tdefer rows.Close()\n",
		1,
	)
	if err := os.WriteFile(storePath, []byte(patchedStore), 0o644); err != nil {
		t.Fatalf("write store.go: %v", err)
	}
	if _, err := ledgerverify.VerifyDir(workDir); err != nil {
		t.Fatalf("VerifyDir after local fix: %v", err)
	}

	runGit(t, workDir, "add", "config/pool.yaml", "pool/store.go")
	runGit(t, workDir, "commit", "-m", "Close row cursor on checksum error and tune pool")
	runGit(t, workDir, "push", "origin", "HEAD:fix/inc-402")

	mergeCode, mergeBody := httpPostJSON(t, opsTS.URL+"/api/pulls", map[string]any{
		"incident_id": "INC-402",
		"branch":      "fix/inc-402",
		"title":       "Close row cursor on checksum error and raise pool limits",
	}, "")
	if mergeCode != http.StatusCreated {
		t.Fatalf("POST /api/pulls = %d (%s), want 201", mergeCode, mergeBody)
	}

	// Verify INC-402 resolved, main branch updated, and live ledger-api recovered.
	_, resolvedRaw := httpGet(t, opsTS.URL+"/api/incidents/INC-402")
	var resolved opshub.Incident
	_ = json.Unmarshal([]byte(resolvedRaw), &resolved)
	if resolved.Status != "RESOLVED" {
		t.Fatalf("INC-402 status after merge = %q, want RESOLVED", resolved.Status)
	}

	freshClone := filepath.Join(t.TempDir(), "fresh")
	runGit(t, "", "clone", opsTS.URL+"/git/ledger-api.git", freshClone)
	if _, err := ledgerverify.VerifyDir(freshClone); err != nil {
		t.Fatalf("fresh clone of main after PR merge failed VerifyDir: %v", err)
	}

	hCode, _ := httpGet(t, ledgerTS.URL+"/healthz")
	if hCode != http.StatusOK {
		t.Fatalf("ledger-api /healthz after PR merge = %d, want 200", hCode)
	}
	txCode, txBody := httpPostJSON(t, ledgerTS.URL+"/api/transfer", map[string]any{
		"tx_id":        "tx-after-pr",
		"amount_cents": 2500,
	}, "")
	if txCode != http.StatusOK {
		t.Fatalf("ledger-api /api/transfer after PR merge = %d (%s), want 200", txCode, txBody)
	}
}

func TestEmptyPullsEndpointSerializesEmptyJSONArray(t *testing.T) {
	hub, err := opshub.NewServer(opshub.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opshub.NewServer: %v", err)
	}
	opsTS := httptest.NewServer(hub.Handler())
	t.Cleanup(opsTS.Close)

	code, raw := httpGet(t, opsTS.URL+"/api/pulls")
	if code != http.StatusOK {
		t.Fatalf("GET /api/pulls status = %d, want 200", code)
	}
	if strings.TrimSpace(raw) != "[]" {
		t.Fatalf("GET /api/pulls raw JSON = %q, want \"[]\"", strings.TrimSpace(raw))
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=SRE Triage Agent",
		"GIT_AUTHOR_EMAIL=sre-agent@example.com",
		"GIT_COMMITTER_NAME=SRE Triage Agent",
		"GIT_COMMITTER_EMAIL=sre-agent@example.com",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func httpGet(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func httpPostJSON(t *testing.T, url string, payload any, bearer string) (int, string) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}
