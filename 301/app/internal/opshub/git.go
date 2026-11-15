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

package opshub

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gke-demos/substrate-agent-platform/app/internal/ledgerverify"
)

const maxGitPackBytes = 10 << 20 // 10 MiB pack upload cap.

var validBranchRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,79}$`)

// CommitSummary describes a commit in ledger-api.git.
type CommitSummary struct {
	SHA       string `json:"sha"`
	Author    string `json:"author"`
	Timestamp string `json:"timestamp"`
	Subject   string `json:"subject"`
}

func initBareLedgerRepo(dataDir string) (string, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("locate git binary: %w", err)
	}

	reposDir := filepath.Join(dataDir, "repos")
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		return "", fmt.Errorf("create repos dir: %w", err)
	}
	bareDir := filepath.Join(reposDir, "ledger-api.git")
	if _, err := os.Stat(filepath.Join(bareDir, "HEAD")); err == nil {
		return bareDir, nil
	}

	if err := runGitCmd(gitBin, "", nil, "init", "--bare", "--initial-branch=main", bareDir); err != nil {
		return "", fmt.Errorf("git init --bare: %w", err)
	}
	if err := runGitCmd(gitBin, bareDir, nil, "config", "http.receivepack", "true"); err != nil {
		return "", fmt.Errorf("git config http.receivepack: %w", err)
	}

	seedDir, err := os.MkdirTemp(dataDir, "seed-ledger-*")
	if err != nil {
		return "", fmt.Errorf("create seed worktree: %w", err)
	}
	defer os.RemoveAll(seedDir)

	if err := ledgerverify.WriteInitialRepoFiles(seedDir); err != nil {
		return "", fmt.Errorf("write initial repo files: %w", err)
	}
	if err := runGitCmd(gitBin, seedDir, nil, "init", "--initial-branch=main"); err != nil {
		return "", fmt.Errorf("git init seed: %w", err)
	}
	if err := runGitCmd(gitBin, seedDir, nil, "add", "."); err != nil {
		return "", fmt.Errorf("git add seed: %w", err)
	}
	if err := runGitCmd(gitBin, seedDir, nil, "commit", "-m", "Initial ledger-api transfer reconciliation service"); err != nil {
		return "", fmt.Errorf("git commit seed: %w", err)
	}
	if err := runGitCmd(gitBin, seedDir, nil, "push", bareDir, "HEAD:refs/heads/main"); err != nil {
		return "", fmt.Errorf("git push seed: %w", err)
	}
	return bareDir, nil
}

func (s *Server) handleGitSmartHTTP(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/git/ledger-api.git")
	if trimmed == r.URL.Path {
		trimmed = strings.TrimPrefix(r.URL.Path, "/git/ledger-api")
	}

	switch {
	case trimmed == "/info/refs" && r.Method == http.MethodGet:
		svc := r.URL.Query().Get("service")
		if svc != "git-upload-pack" && svc != "git-receive-pack" {
			http.Error(w, "unsupported git service", http.StatusForbidden)
			return
		}
		subcmd := strings.TrimPrefix(svc, "git-")
		w.Header().Set("Content-Type", fmt.Sprintf("application/x-%s-advertisement", svc))
		w.Header().Set("Cache-Control", "no-store")

		s.gitMu.Lock()
		out, err := execGitOutput(r.Context(), s.gitBin, s.bareRepoDir, nil, subcmd, "--stateless-rpc", "--advertise-refs", s.bareRepoDir)
		s.gitMu.Unlock()
		if err != nil {
			http.Error(w, "git advertise-refs failed", http.StatusInternalServerError)
			return
		}
		pktHeader := fmt.Sprintf("# service=%s\n", svc)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "%04x%s0000", len(pktHeader)+4, pktHeader)
		_, _ = w.Write(out)

	case (trimmed == "/git-upload-pack" || trimmed == "/git-receive-pack") && r.Method == http.MethodPost:
		svc := strings.TrimPrefix(trimmed, "/")
		subcmd := strings.TrimPrefix(svc, "git-")
		bodyReader := io.Reader(http.MaxBytesReader(w, r.Body, maxGitPackBytes))
		if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
			gz, err := gzip.NewReader(bodyReader)
			if err != nil {
				http.Error(w, "invalid gzip body", http.StatusBadRequest)
				return
			}
			defer gz.Close()
			bodyReader = gz
		}
		payload, err := io.ReadAll(bodyReader)
		if err != nil {
			http.Error(w, "failed reading git pack body", http.StatusRequestEntityTooLarge)
			return
		}

		s.gitMu.Lock()
		out, err := execGitOutput(r.Context(), s.gitBin, s.bareRepoDir, payload, subcmd, "--stateless-rpc", s.bareRepoDir)
		s.gitMu.Unlock()
		if err != nil {
			http.Error(w, "git pack command failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", fmt.Sprintf("application/x-%s-result", svc))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)

	default:
		http.NotFound(w, r)
	}
}

func (s *Server) listRepoCommits(ctx context.Context, branch string, limit int) ([]CommitSummary, error) {
	if branch == "" {
		branch = "main"
	}
	if !isSafeBranchName(branch) {
		return nil, fmt.Errorf("invalid branch name %q", branch)
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	s.gitMu.Lock()
	defer s.gitMu.Unlock()

	ref := "refs/heads/" + branch
	out, err := execGitOutput(ctx, s.gitBin, s.bareRepoDir, nil,
		"log", fmt.Sprintf("-n%d", limit), "--format=%H%x09%an%x09%aI%x09%s", ref)
	if err != nil {
		return nil, fmt.Errorf("branch %q not found", branch)
	}
	var commits []CommitSummary
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			continue
		}
		commits = append(commits, CommitSummary{
			SHA:       parts[0],
			Author:    parts[1],
			Timestamp: parts[2],
			Subject:   parts[3],
		})
	}
	return commits, nil
}

func (s *Server) verifyAndMergeBranch(ctx context.Context, branch string) (*ledgerverify.Report, string, error) {
	if !isSafeBranchName(branch) {
		return nil, "", fmt.Errorf("invalid branch name %q", branch)
	}

	s.gitMu.Lock()
	defer s.gitMu.Unlock()

	ref := "refs/heads/" + branch
	shaOut, err := execGitOutput(ctx, s.gitBin, s.bareRepoDir, nil, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return nil, "", fmt.Errorf("branch %q does not exist in ledger-api.git", branch)
	}
	commitSHA := strings.TrimSpace(string(shaOut))

	tmpDir, err := os.MkdirTemp(s.dataDir, "verify-pr-*")
	if err != nil {
		return nil, "", fmt.Errorf("create verification workspace: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	checkoutDir := filepath.Join(tmpDir, "repo")
	if _, err := execGitOutput(ctx, s.gitBin, "", nil, "clone", "--quiet", "--branch", branch, s.bareRepoDir, checkoutDir); err != nil {
		return nil, "", fmt.Errorf("checkout branch %q: %w", branch, err)
	}

	rep, err := ledgerverify.VerifyDir(checkoutDir)
	if err != nil {
		return rep, commitSHA, err
	}

	if _, err := execGitOutput(ctx, s.gitBin, s.bareRepoDir, nil, "update-ref", "refs/heads/main", commitSHA); err != nil {
		return nil, "", fmt.Errorf("fast-forward main to %s: %w", commitSHA, err)
	}
	return rep, commitSHA, nil
}

func isSafeBranchName(branch string) bool {
	if !validBranchRef.MatchString(branch) {
		return false
	}
	if strings.Contains(branch, "..") || strings.HasSuffix(branch, ".lock") || strings.HasSuffix(branch, "/") {
		return false
	}
	return true
}

func runGitCmd(gitBin, dir string, stdin []byte, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := execGitOutput(ctx, gitBin, dir, stdin, args...)
	return err
}

func execGitOutput(ctx context.Context, gitBin, dir string, stdin []byte, args ...string) ([]byte, error) {
	if gitBin == "" {
		return nil, errors.New("git binary not configured")
	}
	cmd := exec.CommandContext(ctx, gitBin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	home := os.TempDir()
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Operations Hub",
		"GIT_AUTHOR_EMAIL=ops-hub@example.com",
		"GIT_COMMITTER_NAME=Operations Hub",
		"GIT_COMMITTER_EMAIL=ops-hub@example.com",
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
