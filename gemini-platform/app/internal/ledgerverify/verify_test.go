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

package ledgerverify_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/ledgerverify"
)

func validFixedRepo(t *testing.T, mods ...func(dir string)) string {
	t.Helper()
	dir := t.TempDir()
	if err := ledgerverify.WriteInitialRepoFiles(dir); err != nil {
		t.Fatalf("WriteInitialRepoFiles: %v", err)
	}
	fixedPoolYAML := strings.Join([]string{
		"max_open_conns: 32",
		"idle_timeout_seconds: 30",
		"acquire_timeout_ms: 150",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "config", "pool.yaml"), []byte(fixedPoolYAML), 0o644); err != nil {
		t.Fatalf("write fixed pool.yaml: %v", err)
	}
	storePath := filepath.Join(dir, "pool", "store.go")
	raw, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	patched := strings.Replace(
		string(raw),
		"if err != nil {\n\t\treturn err\n\t}\n",
		"if err != nil {\n\t\treturn err\n\t}\n\tdefer rows.Close()\n",
		1,
	)
	if patched == string(raw) {
		t.Fatalf("expected initial store.go to match patch anchor")
	}
	if err := os.WriteFile(storePath, []byte(patched), 0o644); err != nil {
		t.Fatalf("write patched store.go: %v", err)
	}
	for _, mod := range mods {
		mod(dir)
	}
	return dir
}

func TestVerifyDir(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T) string
		wantOK      bool
		wantSubstrs []string
	}{
		{
			name:   "valid remediated repository",
			setup:  func(t *testing.T) string { return validFixedRepo(t) },
			wantOK: true,
		},
		{
			name: "valid lower bounds on max_open_conns and idle_timeout_seconds",
			setup: func(t *testing.T) string {
				return validFixedRepo(t, func(dir string) {
					cfg := "max_open_conns: 16\nidle_timeout_seconds: 5\nacquire_timeout_ms: 100\n"
					_ = os.WriteFile(filepath.Join(dir, "config", "pool.yaml"), []byte(cfg), 0o644)
				})
			},
			wantOK: true,
		},
		{
			name: "invalid initial buggy repository",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				if err := ledgerverify.WriteInitialRepoFiles(dir); err != nil {
					t.Fatalf("WriteInitialRepoFiles: %v", err)
				}
				return dir
			},
			wantSubstrs: []string{"max_open_conns", "idle_timeout_seconds", "defer rows.Close()"},
		},
		{
			name: "invalid max_open_conns just below minimum",
			setup: func(t *testing.T) string {
				return validFixedRepo(t, func(dir string) {
					cfg := "max_open_conns: 15\nidle_timeout_seconds: 30\nacquire_timeout_ms: 150\n"
					_ = os.WriteFile(filepath.Join(dir, "config", "pool.yaml"), []byte(cfg), 0o644)
				})
			},
			wantSubstrs: []string{"max_open_conns"},
		},
		{
			name: "invalid idle_timeout_seconds zero",
			setup: func(t *testing.T) string {
				return validFixedRepo(t, func(dir string) {
					cfg := "max_open_conns: 32\nidle_timeout_seconds: 0\nacquire_timeout_ms: 150\n"
					_ = os.WriteFile(filepath.Join(dir, "config", "pool.yaml"), []byte(cfg), 0o644)
				})
			},
			wantSubstrs: []string{"idle_timeout_seconds"},
		},
		{
			name: "invalid store.go syntax error",
			setup: func(t *testing.T) string {
				return validFixedRepo(t, func(dir string) {
					_ = os.WriteFile(filepath.Join(dir, "pool", "store.go"), []byte("package pool\nfunc broken( {\n"), 0o644)
				})
			},
			wantSubstrs: []string{"pool/store.go"},
		},
		{
			name: "invalid symlink escape on pool.yaml",
			setup: func(t *testing.T) string {
				outside := filepath.Join(t.TempDir(), "outside.yaml")
				_ = os.WriteFile(outside, []byte("max_open_conns: 32\nidle_timeout_seconds: 30\nacquire_timeout_ms: 150\n"), 0o644)
				return validFixedRepo(t, func(dir string) {
					target := filepath.Join(dir, "config", "pool.yaml")
					_ = os.Remove(target)
					_ = os.Symlink(outside, target)
				})
			},
			wantSubstrs: []string{"symlink"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setup(t)
			rep, err := ledgerverify.VerifyDir(dir)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("VerifyDir(%s) unexpected error: %v", dir, err)
				}
				if !rep.OK || !rep.DeferClosePresent {
					t.Fatalf("VerifyDir(%s) = %+v, want OK=true and DeferClosePresent=true", dir, rep)
				}
				return
			}
			if err == nil {
				t.Fatalf("VerifyDir(%s) succeeded (%+v), want error containing %v", dir, rep, tt.wantSubstrs)
			}
			for _, sub := range tt.wantSubstrs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("VerifyDir(%s) error %q missing substring %q", dir, err.Error(), sub)
				}
			}
		})
	}
}

func TestLedgerVerifyCLI(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "ledger-verify")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/ledger-verify")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ledger-verify: %v\n%s", err, out)
	}

	buggyDir := t.TempDir()
	if err := ledgerverify.WriteInitialRepoFiles(buggyDir); err != nil {
		t.Fatalf("WriteInitialRepoFiles: %v", err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, buggyDir)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("ledger-verify on buggy repo exited 0, want non-zero")
	}
	if stdout.Len() != 0 {
		t.Errorf("ledger-verify stdout on error = %q, want empty stdout", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Error:") {
		t.Errorf("ledger-verify stderr = %q, want Error: prefix", stderr.String())
	}

	fixedDir := validFixedRepo(t)
	stdout.Reset()
	stderr.Reset()
	cmd = exec.Command(bin, fixedDir)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ledger-verify on fixed repo failed: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "VERIFIED") {
		t.Errorf("ledger-verify stdout = %q, want VERIFIED confirmation", stdout.String())
	}
}
