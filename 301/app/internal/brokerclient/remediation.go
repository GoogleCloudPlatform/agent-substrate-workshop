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
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ledgerverify"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/telemetry"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	remediationRepoDirName = "ledger-api"
	// RemediationGateTokenHeader carries the one-time human Remediation Gate ticket
	// issued by sre-gateway's handleRemediationApprove across POST /mcp/ops-hub-mcp.
	RemediationGateTokenHeader = "X-Remediation-Gate-Token"
)

type gateApprovalTokenKey struct{}

// ContextWithGateApprovalToken attaches a one-time Remediation Gate approval ticket
// to ctx for the resumed ADK confirmation turn.
func ContextWithGateApprovalToken(ctx context.Context, tok string) context.Context {
	trimmed := strings.TrimSpace(tok)
	if trimmed == "" {
		return ctx
	}
	return context.WithValue(ctx, gateApprovalTokenKey{}, trimmed)
}

// GateApprovalTokenFromContext extracts the one-time Remediation Gate approval
// ticket from ctx, if present.
func GateApprovalTokenFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(gateApprovalTokenKey{}).(string)
	return strings.TrimSpace(v)
}

var safeRemediationBranchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,79}$`)

// PushGitBranchArgs defines input parameters for push_git_branch.
type PushGitBranchArgs struct {
	Branch string `json:"branch,omitempty" jsonschema:"Remediation git branch inside /workspace/ledger-api to push to Operations Hub (e.g. fix/inc-402-pool-leak)."`
}

// PushGitBranchResult reports the outcome of pushing a verified remediation branch.
type PushGitBranchResult struct {
	Pushed    bool   `json:"pushed"`
	Branch    string `json:"branch"`
	CommitSHA string `json:"commit_sha,omitempty"`
	RemoteURL string `json:"remote_url,omitempty"`
	VerifyOK  bool   `json:"verify_ok"`
	Error     string `json:"error,omitempty"`
}

// ApplyRemediationPRArgs defines input parameters for apply_remediation_pr.
type ApplyRemediationPRArgs struct {
	IncidentID string `json:"incident_id" jsonschema:"Incident identifier resolved by this remediation pull request (e.g. INC-402)."`
	Branch     string `json:"branch" jsonschema:"Pushed git remediation branch name in ledger-api.git (e.g. fix/inc-402-pool-leak)."`
	Title      string `json:"title" jsonschema:"Concise pull request summary title."`
}

// ApplyRemediationPRResult reports the merged PR metadata returned by ops-hub-mcp.
type ApplyRemediationPRResult struct {
	Merged     bool   `json:"merged"`
	PRID       string `json:"pr_id,omitempty"`
	IncidentID string `json:"incident_id"`
	Branch     string `json:"branch"`
	Title      string `json:"title"`
	Status     string `json:"status,omitempty"`
	CommitSHA  string `json:"commit_sha,omitempty"`
}

// NewRemediationTools constructs push_git_branch and the confirmation-gated
// apply_remediation_pr ADK functiontools (`RequireConfirmation: true`) routed
// strictly through the Governed Broker.
func NewRemediationTools(brokerURL, workspaceDir string, hc *http.Client) ([]tool.Tool, error) {
	base := strings.TrimRight(strings.TrimSpace(brokerURL), "/")
	if base == "" {
		return nil, errors.New("brokerclient: BROKER_URL is required")
	}
	ws := strings.TrimSpace(workspaceDir)
	if ws == "" {
		ws = "/workspace"
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return nil, fmt.Errorf("brokerclient: create workspace directory %s: %w", ws, err)
	}
	absWS, err := filepath.Abs(filepath.Clean(ws))
	if err != nil {
		return nil, fmt.Errorf("brokerclient: resolve workspace path: %w", err)
	}
	evalWS, err := filepath.EvalSymlinks(absWS)
	if err != nil {
		return nil, fmt.Errorf("brokerclient: evaluate workspace symlinks: %w", err)
	}
	hc = telemetry.WrapHTTPClient(hc, 30*time.Second)

	r := &remediationExecutor{
		baseURL:      base,
		workspaceDir: evalWS,
		hc:           hc,
	}

	pushTool, err := functiontool.New(functiontool.Config{
		Name:        "push_git_branch",
		Description: "Verify /workspace/ledger-api with ledger-verify and push the staged git remediation branch to Operations Hub via the Governed Broker.",
	}, func(ctx agent.Context, in PushGitBranchArgs) (PushGitBranchResult, error) {
		return r.pushBranch(ctx, in.Branch)
	})
	if err != nil {
		return nil, fmt.Errorf("brokerclient: create push_git_branch tool: %w", err)
	}

	applyTool, err := functiontool.New(functiontool.Config{
		Name:                "apply_remediation_pr",
		Description:         "Merge a pushed git remediation branch into main via Operations Hub MCP and reload ledger-api configuration (pauses at the Remediation Gate for human operator approval).",
		RequireConfirmation: true,
	}, func(ctx agent.Context, in ApplyRemediationPRArgs) (ApplyRemediationPRResult, error) {
		return r.applyRemediationPR(ctx, in)
	})
	if err != nil {
		return nil, fmt.Errorf("brokerclient: create apply_remediation_pr tool: %w", err)
	}

	return []tool.Tool{pushTool, applyTool}, nil
}

type remediationExecutor struct {
	baseURL      string
	workspaceDir string
	hc           *http.Client
}

func (r *remediationExecutor) resolveRepoDir() (string, error) {
	repoDir := filepath.Join(r.workspaceDir, remediationRepoDirName)
	prefix := r.workspaceDir + string(filepath.Separator)
	if !strings.HasPrefix(repoDir, prefix) {
		return "", errors.New("repository path escapes workspace boundary")
	}
	st, err := os.Lstat(repoDir)
	if err != nil {
		return "", fmt.Errorf("ledger-api checkout not found in %s; run clone_repo first", r.workspaceDir)
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return "", errors.New("ledger-api path is not a regular directory")
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return "", errors.New("ledger-api git metadata missing; run clone_repo first")
	}
	return repoDir, nil
}

func validateRemediationBranch(branch string) error {
	if !safeRemediationBranchPattern.MatchString(branch) ||
		strings.Contains(branch, "..") ||
		strings.HasSuffix(branch, ".lock") ||
		strings.HasSuffix(branch, "/") ||
		branch == "main" ||
		branch == "master" {
		return fmt.Errorf("invalid remediation branch name %q", branch)
	}
	return nil
}

func (r *remediationExecutor) pushBranch(ctx context.Context, rawBranch string) (PushGitBranchResult, error) {
	repoDir, err := r.resolveRepoDir()
	if err != nil {
		return PushGitBranchResult{Error: err.Error()}, nil
	}
	if _, verifyErr := ledgerverify.VerifyDir(repoDir); verifyErr != nil {
		return PushGitBranchResult{
			VerifyOK: false,
			Error:    "ledger-verify failed before push: " + verifyErr.Error(),
		}, nil
	}

	gitBin, err := exec.LookPath("git")
	if err != nil {
		return PushGitBranchResult{}, fmt.Errorf("locate git binary: %w", err)
	}
	clearRemediationGitLocks(repoDir)

	branch := strings.TrimSpace(rawBranch)
	if branch == "" {
		curBranch, _ := runRemediationGit(ctx, gitBin, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
		branch = strings.TrimSpace(curBranch)
	}
	if branch == "" || branch == "HEAD" || branch == "main" {
		branch = "fix/inc-402-pool-leak"
	}
	if err := validateRemediationBranch(branch); err != nil {
		return PushGitBranchResult{Branch: branch, VerifyOK: true, Error: err.Error()}, nil
	}

	remoteURL := r.baseURL + "/ops/git/ledger-api.git"
	if _, err := runRemediationGit(ctx, gitBin, repoDir, "push", "--force", remoteURL, "HEAD:refs/heads/"+branch); err != nil {
		return PushGitBranchResult{
			Branch:    branch,
			RemoteURL: remoteURL,
			VerifyOK:  true,
			Error:     err.Error(),
		}, nil
	}
	shaOut, _ := runRemediationGit(ctx, gitBin, repoDir, "rev-parse", "HEAD")
	commitSHA := strings.TrimSpace(shaOut)
	return PushGitBranchResult{
		Pushed:    true,
		Branch:    branch,
		CommitSHA: commitSHA,
		RemoteURL: remoteURL,
		VerifyOK:  true,
	}, nil
}

func (r *remediationExecutor) applyRemediationPR(ctx context.Context, in ApplyRemediationPRArgs) (ApplyRemediationPRResult, error) {
	incID := strings.ToUpper(strings.TrimSpace(in.IncidentID))
	if incID == "" {
		incID = "INC-402"
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = "fix/inc-402-pool-leak"
	}
	if err := validateRemediationBranch(branch); err != nil {
		return ApplyRemediationPRResult{}, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Fix ledger-api connection pool sizing and close rows cursor on checksum error"
	}

	// Re-push the local verified branch only if ops-hub lacks this branch tip (for
	// example after deploy/ops-hub rolls with OPS_HUB_REQUIRE_3LO=true across a
	// SNAPSHOT_FIDELITY_MEMORY restore), avoiding redundant git subprocess forks on
	// normal Remediation Gate approvals.
	if repoDir, err := r.resolveRepoDir(); err == nil {
		if !r.remoteHasBranchHead(ctx, repoDir, branch) {
			_, _ = r.pushBranch(ctx, branch)
		}
	}

	rpcBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "apply_remediation_pr",
			"arguments": map[string]any{
				"incident_id": incID,
				"branch":      branch,
				"title":       title,
			},
		},
	})
	if err != nil {
		return ApplyRemediationPRResult{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/mcp/ops-hub-mcp", bytes.NewReader(rpcBody))
	if err != nil {
		return ApplyRemediationPRResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Remediation-Gate-Confirmed", "true")
	if gateTok := GateApprovalTokenFromContext(ctx); gateTok != "" {
		httpReq.Header.Set(RemediationGateTokenHeader, gateTok)
	}

	resp, err := r.hc.Do(httpReq)
	if err != nil {
		return ApplyRemediationPRResult{}, fmt.Errorf("call /mcp/ops-hub-mcp apply_remediation_pr: %w", err)
	}
	defer resp.Body.Close()

	rawResp, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ApplyRemediationPRResult{}, fmt.Errorf("ops-hub-mcp apply_remediation_pr HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(rawResp)))
	}

	var rpcOut struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rawResp, &rpcOut); err != nil {
		return ApplyRemediationPRResult{}, fmt.Errorf("decode ops-hub-mcp response: %w", err)
	}
	if rpcOut.Error != nil {
		return ApplyRemediationPRResult{}, fmt.Errorf("ops-hub-mcp error (%d): %s", rpcOut.Error.Code, rpcOut.Error.Message)
	}
	var text string
	if len(rpcOut.Result.Content) > 0 {
		text = strings.TrimSpace(rpcOut.Result.Content[0].Text)
	}
	if rpcOut.Result.IsError {
		return ApplyRemediationPRResult{}, fmt.Errorf("apply_remediation_pr failed: %s", text)
	}

	var prDoc struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		CommitSHA string `json:"commit_sha"`
	}
	_ = json.Unmarshal([]byte(text), &prDoc)
	return ApplyRemediationPRResult{
		Merged:     true,
		PRID:       prDoc.ID,
		IncidentID: incID,
		Branch:     branch,
		Title:      title,
		Status:     prDoc.Status,
		CommitSHA:  prDoc.CommitSHA,
	}, nil
}

func (r *remediationExecutor) remoteHasBranchHead(ctx context.Context, repoDir, branch string) bool {
	localSHA := resolveLocalGitHeadSHA(ctx, repoDir)
	if len(localSHA) < 40 {
		return false
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, r.baseURL+"/ops/git/ledger-api.git/info/refs?service=git-upload-pack", nil)
	if err != nil {
		return false
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return false
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return strings.Contains(string(raw), localSHA+" refs/heads/"+branch)
}

func resolveLocalGitHeadSHA(ctx context.Context, repoDir string) string {
	gitDir := filepath.Join(repoDir, ".git")
	headBytes, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err == nil {
		head := strings.TrimSpace(string(headBytes))
		if ref, ok := strings.CutPrefix(head, "ref:"); ok {
			refPath := filepath.Join(gitDir, filepath.FromSlash(strings.TrimSpace(ref)))
			if refBytes, refErr := os.ReadFile(refPath); refErr == nil {
				if sha := strings.TrimSpace(string(refBytes)); len(sha) >= 40 {
					return sha
				}
			}
		} else if len(head) >= 40 {
			return head
		}
	}
	if gitBin, lookErr := exec.LookPath("git"); lookErr == nil {
		if out, revErr := runRemediationGit(ctx, gitBin, repoDir, "rev-parse", "HEAD"); revErr == nil {
			return strings.TrimSpace(out)
		}
	}
	return ""
}

func clearRemediationGitLocks(repoDir string) {
	gitDir := filepath.Join(repoDir, ".git")
	_ = os.Remove(filepath.Join(gitDir, "index.lock"))
	_ = os.Remove(filepath.Join(gitDir, "HEAD.lock"))
	_ = filepath.WalkDir(filepath.Join(gitDir, "refs"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".lock") {
			_ = os.Remove(p)
		}
		return nil
	})
}

func runRemediationGit(ctx context.Context, gitBin, dir string, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, gitBin, args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=SRE Incident Actor",
		"GIT_AUTHOR_EMAIL=sre-actor@example.com",
		"GIT_COMMITTER_NAME=SRE Incident Actor",
		"GIT_COMMITTER_EMAIL=sre-actor@example.com",
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
