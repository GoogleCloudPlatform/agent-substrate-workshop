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

package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ledgerverify"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	maxWorkspaceFileBytes = 1 << 20 // 1 MiB cap per read or written file.
	defaultRepoName       = "ledger-api"
)

var (
	safeBranchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,79}$`)
	allowedGitSubcmds = map[string]bool{
		"status":    true,
		"diff":      true,
		"log":       true,
		"branch":    true,
		"rev-parse": true,
		"show":      true,
	}
)

// WorkspaceState describes the durable /workspace git checkout inside an Incident Actor.
type WorkspaceState struct {
	WorkspaceDir   string               `json:"workspace_dir"`
	Cloned         bool                 `json:"cloned"`
	RepoDir        string               `json:"repo_dir,omitempty"`
	Branch         string               `json:"branch,omitempty"`
	HeadSHA        string               `json:"head_sha,omitempty"`
	HeadSubject    string               `json:"head_subject,omitempty"`
	StatusShort    string               `json:"status_short,omitempty"`
	Diff           string               `json:"diff,omitempty"`
	VerifyOK       bool                 `json:"verify_ok"`
	VerifyFindings []string             `json:"verify_findings,omitempty"`
	VerifyReport   *ledgerverify.Report `json:"verify_report,omitempty"`
}

// Workspace confines repository clones, file edits, and allowlisted commands to rootDir.
type Workspace struct {
	mu        sync.Mutex
	rootDir   string
	brokerURL string
}

// NewWorkspace initializes a Workspace rooted at rootDir.
func NewWorkspace(rootDir, brokerURL string) (*Workspace, error) {
	if strings.TrimSpace(rootDir) == "" {
		return nil, errors.New("workspace root directory is empty")
	}
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace directory %s: %w", rootDir, err)
	}
	absRoot, err := filepath.Abs(filepath.Clean(rootDir))
	if err != nil {
		return nil, fmt.Errorf("resolve workspace path: %w", err)
	}
	evalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("evaluate workspace symlinks: %w", err)
	}
	return &Workspace{
		rootDir:   evalRoot,
		brokerURL: strings.TrimRight(strings.TrimSpace(brokerURL), "/"),
	}, nil
}

// RootDir returns the canonical workspace root path.
func (w *Workspace) RootDir() string { return w.rootDir }

// resolveInside verifies that relPath stays strictly inside w.rootDir with zero symlink traversal.
func (w *Workspace) resolveInside(relPath string, allowMissingLeaf bool) (string, error) {
	trimmed := strings.TrimSpace(relPath)
	if trimmed == "" || trimmed == "." {
		return w.rootDir, nil
	}
	cleaned := filepath.Clean(trimmed)
	if filepath.IsAbs(cleaned) {
		if cleaned == w.rootDir {
			return w.rootDir, nil
		}
		prefix := w.rootDir + string(filepath.Separator)
		if !strings.HasPrefix(cleaned, prefix) {
			return "", fmt.Errorf("path %q escapes workspace boundary", relPath)
		}
		cleaned = strings.TrimPrefix(cleaned, prefix)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace boundary", relPath)
	}

	full := filepath.Join(w.rootDir, cleaned)
	prefix := w.rootDir + string(filepath.Separator)
	if full != w.rootDir && !strings.HasPrefix(full, prefix) {
		return "", fmt.Errorf("path %q escapes workspace boundary", relPath)
	}

	parts := strings.Split(cleaned, string(filepath.Separator))
	cur := w.rootDir
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) && allowMissingLeaf {
				continue
			}
			if os.IsNotExist(err) && i == len(parts)-1 {
				return full, nil
			}
			return "", fmt.Errorf("%s: %w", filepath.ToSlash(cleaned), err)
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path %q escapes workspace boundary via symlink", relPath)
		}
	}
	return full, nil
}

// CloneRepoArgs defines input parameters for clone_repo.
type CloneRepoArgs struct {
	Repo    string `json:"repo,omitempty" jsonschema:"Repository name or governed git URL to clone into /workspace (defaults to ledger-api)."`
	RepoURL string `json:"repo_url,omitempty" jsonschema:"Optional explicit git URL routed via the Governed Broker."`
}

// CloneRepoResult summarizes the cloned checkout in /workspace.
type CloneRepoResult struct {
	Cloned      bool     `json:"cloned"`
	RepoDir     string   `json:"repo_dir"`
	SourceURL   string   `json:"source_url"`
	HeadSHA     string   `json:"head_sha"`
	HeadSubject string   `json:"head_subject"`
	Files       []string `json:"files"`
}

// CloneRepo clones or refreshes ledger-api.git inside /workspace/ledger-api.
func (w *Workspace) CloneRepo(ctx context.Context, in CloneRepoArgs) (CloneRepoResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	gitURL, err := w.resolveCloneURL(in.Repo, in.RepoURL)
	if err != nil {
		return CloneRepoResult{}, err
	}
	targetDir, err := w.resolveInside(defaultRepoName, true)
	if err != nil {
		return CloneRepoResult{}, err
	}

	gitBin, err := exec.LookPath("git")
	if err != nil {
		return CloneRepoResult{}, fmt.Errorf("locate git binary: %w", err)
	}

	if _, statErr := os.Stat(filepath.Join(targetDir, ".git")); statErr != nil {
		_ = os.RemoveAll(targetDir)
		if _, err := runScopedGit(ctx, gitBin, w.rootDir, "clone", "--quiet", gitURL, targetDir); err != nil {
			return CloneRepoResult{}, fmt.Errorf("git clone %s: %w", gitURL, err)
		}
	} else {
		clearStaleGitLocks(targetDir)
		_, _ = runScopedGit(ctx, gitBin, targetDir, "remote", "set-url", "origin", gitURL)
		_, _ = runScopedGit(ctx, gitBin, targetDir, "fetch", "--quiet", "origin")
	}

	shaOut, _ := runScopedGit(ctx, gitBin, targetDir, "rev-parse", "--short", "HEAD")
	subjOut, _ := runScopedGit(ctx, gitBin, targetDir, "log", "-1", "--format=%s")
	files, _ := listRepoRelativeFiles(targetDir)

	return CloneRepoResult{
		Cloned:      true,
		RepoDir:     defaultRepoName,
		SourceURL:   gitURL,
		HeadSHA:     strings.TrimSpace(shaOut),
		HeadSubject: strings.TrimSpace(subjOut),
		Files:       files,
	}, nil
}

func (w *Workspace) resolveCloneURL(repo, explicitURL string) (string, error) {
	raw := strings.TrimSpace(explicitURL)
	if raw == "" {
		raw = strings.TrimSpace(repo)
	}
	if w.brokerURL != "" {
		// Route Operations Hub git traffic strictly through sre-gateway/ops so
		// actor egress stays confined to sre-gateway.${NAMESPACE}.svc.cluster.local.
		if raw == "" || raw == "ledger-api" || raw == "ledger-api.git" ||
			strings.Contains(raw, "/git/ledger-api") {
			return w.brokerURL + "/ops/git/ledger-api.git", nil
		}
		if strings.HasPrefix(raw, w.brokerURL+"/ops/git/") {
			return raw, nil
		}
		return "", fmt.Errorf("repository URL %q is not routed via governed broker %s", raw, w.brokerURL)
	}
	if strings.HasPrefix(raw, "http://127.0.0.1:") || strings.HasPrefix(raw, "http://localhost:") {
		return raw, nil
	}
	return "", errors.New("BROKER_URL is required to clone from Operations Hub")
}

// ReadFileArgs defines input parameters for read_workspace_file.
type ReadFileArgs struct {
	Path string `json:"path" jsonschema:"Relative file or directory path inside /workspace, such as ledger-api/config/pool.yaml or ledger-api/pool/store.go."`
}

// ReadFileResult returns file contents or directory entries inside /workspace.
type ReadFileResult struct {
	Path    string   `json:"path"`
	IsDir   bool     `json:"is_dir,omitempty"`
	Entries []string `json:"entries,omitempty"`
	Content string   `json:"content,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// ReadFile reads a bounded regular file or directory listing inside /workspace.
func (w *Workspace) ReadFile(_ context.Context, in ReadFileArgs) (ReadFileResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	target, err := w.resolveInside(in.Path, false)
	if err != nil {
		return ReadFileResult{Path: in.Path, Error: err.Error()}, nil
	}
	st, err := os.Lstat(target)
	if err != nil {
		return ReadFileResult{Path: in.Path, Error: err.Error()}, nil
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return ReadFileResult{Path: in.Path, Error: "path escapes workspace boundary via symlink"}, nil
	}
	if st.IsDir() {
		entries, err := os.ReadDir(target)
		if err != nil {
			return ReadFileResult{Path: in.Path, Error: err.Error()}, nil
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			names = append(names, name)
		}
		sort.Strings(names)
		return ReadFileResult{Path: in.Path, IsDir: true, Entries: names}, nil
	}
	if !st.Mode().IsRegular() {
		return ReadFileResult{Path: in.Path, Error: "path is not a regular file"}, nil
	}
	if st.Size() > maxWorkspaceFileBytes {
		return ReadFileResult{Path: in.Path, Error: fmt.Sprintf("file exceeds %d byte cap", maxWorkspaceFileBytes)}, nil
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return ReadFileResult{Path: in.Path, Error: err.Error()}, nil
	}
	return ReadFileResult{Path: in.Path, Content: string(b)}, nil
}

// RunCommandArgs defines input parameters for run_workspace_command.
type RunCommandArgs struct {
	Command string   `json:"command" jsonschema:"Allowlisted executable to run: ledger-verify, git, or sh (restricted to unpacked .skills/ scripts)."`
	Args    []string `json:"args,omitempty" jsonschema:"Arguments passed directly without shell interpolation."`
	Dir     string   `json:"dir,omitempty" jsonschema:"Relative working directory inside /workspace (defaults to ledger-api)."`
}

// RunCommandResult captures stdout/stderr and exit status from an allowlisted command.
type RunCommandResult struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	Allowed  bool   `json:"allowed"`
}

// RunCommand executes strictly allowlisted commands confined to /workspace.
func (w *Workspace) RunCommand(ctx context.Context, in RunCommandArgs) (RunCommandResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	relDir := strings.TrimSpace(in.Dir)
	if relDir == "" {
		relDir = defaultRepoName
	}
	workDir, err := w.resolveInside(relDir, false)
	if err != nil {
		return RunCommandResult{
			Command:  in.Command,
			ExitCode: 1,
			Output:   err.Error(),
			Allowed:  false,
		}, nil
	}
	st, err := os.Stat(workDir)
	if err != nil || !st.IsDir() {
		return RunCommandResult{
			Command:  in.Command,
			ExitCode: 1,
			Output:   fmt.Sprintf("working directory %q does not exist in /workspace", relDir),
			Allowed:  false,
		}, nil
	}

	bin := strings.TrimSpace(in.Command)
	switch bin {
	case "ledger-verify":
		rep, verifyErr := ledgerverify.VerifyDir(workDir)
		raw, _ := json.MarshalIndent(rep, "", "  ")
		code := 0
		if verifyErr != nil {
			code = 1
		}
		return RunCommandResult{
			Command:  "ledger-verify",
			ExitCode: code,
			Output:   string(raw),
			Allowed:  true,
		}, nil

	case "git":
		if len(in.Args) == 0 || !allowedGitSubcmds[in.Args[0]] {
			return RunCommandResult{
				Command:  "git",
				ExitCode: 126,
				Output:   fmt.Sprintf("git subcommand in %v is not allowlisted (allowed: status, diff, log, branch, rev-parse, show)", in.Args),
				Allowed:  false,
			}, nil
		}
		for _, a := range in.Args {
			if strings.HasPrefix(a, "--upload-pack") || strings.HasPrefix(a, "--exec") || a == "-c" {
				return RunCommandResult{
					Command:  "git",
					ExitCode: 126,
					Output:   fmt.Sprintf("git flag %q is not allowlisted", a),
					Allowed:  false,
				}, nil
			}
		}
		gitBin, err := exec.LookPath("git")
		if err != nil {
			return RunCommandResult{}, fmt.Errorf("locate git: %w", err)
		}
		out, runErr := runScopedGit(ctx, gitBin, workDir, in.Args...)
		code := 0
		if runErr != nil {
			code = 1
			out = out + "\n" + runErr.Error()
		}
		return RunCommandResult{
			Command:  "git",
			ExitCode: code,
			Output:   strings.TrimSpace(out),
			Allowed:  true,
		}, nil

	case "sh":
		if len(in.Args) == 0 {
			return RunCommandResult{
				Command:  "sh",
				ExitCode: 126,
				Output:   "sh requires a script path inside .skills/",
				Allowed:  false,
			}, nil
		}
		scriptPath, err := w.resolveSkillScriptPath(in.Args[0])
		if err != nil {
			return RunCommandResult{
				Command:  "sh",
				ExitCode: 126,
				Output:   err.Error(),
				Allowed:  false,
			}, nil
		}
		shBin, err := exec.LookPath("sh")
		if err != nil {
			return RunCommandResult{}, fmt.Errorf("locate sh: %w", err)
		}
		cmdCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmdArgs := append([]string{scriptPath}, in.Args[1:]...)
		cmd := exec.CommandContext(cmdCtx, shBin, cmdArgs...)
		cmd.Dir = workDir
		cmd.Env = []string{
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"HOME=" + w.rootDir,
			"WORKSPACE_DIR=" + w.rootDir,
		}
		var combined bytes.Buffer
		cmd.Stdout = &combined
		cmd.Stderr = &combined
		runErr := cmd.Run()
		code := 0
		if runErr != nil {
			code = 1
		}
		return RunCommandResult{
			Command:  "sh",
			ExitCode: code,
			Output:   strings.TrimSpace(combined.String()),
			Allowed:  true,
		}, nil

	default:
		return RunCommandResult{
			Command:  bin,
			ExitCode: 126,
			Output:   fmt.Sprintf("command %q is not allowlisted (allowed: ledger-verify, git, sh on .skills/ scripts)", bin),
			Allowed:  false,
		}, nil
	}
}

func (w *Workspace) resolveSkillScriptPath(rel string) (string, error) {
	normalized := strings.TrimSpace(rel)
	slashNorm := filepath.ToSlash(normalized)
	switch {
	case strings.HasPrefix(slashNorm, "/workspace/.skills/"):
		normalized = strings.TrimPrefix(slashNorm, "/workspace/")
	case strings.HasPrefix(slashNorm, "../.skills/"):
		normalized = strings.TrimPrefix(slashNorm, "../")
	}
	skillsRoot := filepath.Join(w.rootDir, ".skills")
	resolved, err := w.resolveInside(normalized, false)
	if err != nil {
		return "", fmt.Errorf("skill script path %q is not allowlisted: %w", rel, err)
	}
	prefix := skillsRoot + string(filepath.Separator)
	if !strings.HasPrefix(resolved, prefix) || !strings.HasSuffix(resolved, ".sh") {
		return "", fmt.Errorf("sh execution is restricted to .sh scripts under /workspace/.skills/ (got %q)", rel)
	}
	st, err := os.Lstat(resolved)
	if err != nil || !st.Mode().IsRegular() {
		return "", fmt.Errorf("skill script %q does not exist or is not a regular file", rel)
	}
	return resolved, nil
}

// StagePatchArgs defines input parameters for stage_git_patch.
type StagePatchArgs struct {
	Branch        string            `json:"branch" jsonschema:"Remediation branch name to create or update, e.g. fix/inc-402-pool."`
	CommitMessage string            `json:"commit_message" jsonschema:"Imperative git commit message summarizing the fix."`
	Files         map[string]string `json:"files" jsonschema:"Map of relative paths inside ledger-api (e.g. config/pool.yaml, pool/store.go) to full updated file contents."`
}

// StagePatchResult reports verification and commit metadata after staging a patch.
type StagePatchResult struct {
	Staged         bool                 `json:"staged"`
	Branch         string               `json:"branch"`
	CommitSHA      string               `json:"commit_sha,omitempty"`
	Diff           string               `json:"diff,omitempty"`
	VerifyOK       bool                 `json:"verify_ok"`
	VerifyReport   *ledgerverify.Report `json:"verify_report,omitempty"`
	VerifyFindings []string             `json:"verify_findings,omitempty"`
	Error          string               `json:"error,omitempty"`
}

// StagePatch writes updated files inside /workspace/ledger-api, verifies invariants, and commits on branch.
func (w *Workspace) StagePatch(ctx context.Context, in StagePatchArgs) (StagePatchResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = "fix/inc-402-pool"
	}
	if !safeBranchPattern.MatchString(branch) || strings.Contains(branch, "..") || strings.HasSuffix(branch, ".lock") {
		return StagePatchResult{Branch: branch, Error: fmt.Sprintf("invalid branch name %q", branch)}, nil
	}
	msg := strings.TrimSpace(in.CommitMessage)
	if msg == "" {
		msg = "Fix ledger-api connection pool sizing and row cursor leak"
	}
	if len(in.Files) == 0 {
		return StagePatchResult{Branch: branch, Error: "files map must not be empty"}, nil
	}

	repoDir, err := w.resolveInside(defaultRepoName, false)
	if err != nil {
		return StagePatchResult{Branch: branch, Error: err.Error()}, nil
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return StagePatchResult{Branch: branch, Error: "ledger-api repository is not cloned in /workspace; call clone_repo first"}, nil
	}

	for rel, content := range in.Files {
		if len(content) > maxWorkspaceFileBytes {
			return StagePatchResult{Branch: branch, Error: fmt.Sprintf("file %q exceeds %d byte cap", rel, maxWorkspaceFileBytes)}, nil
		}
		cleanRel := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(rel)), defaultRepoName+"/")
		full, err := w.resolveInside(filepath.Join(defaultRepoName, cleanRel), true)
		if err != nil {
			return StagePatchResult{Branch: branch, Error: err.Error()}, nil
		}
		repoPrefix := repoDir + string(filepath.Separator)
		if !strings.HasPrefix(full, repoPrefix) {
			return StagePatchResult{Branch: branch, Error: fmt.Sprintf("path %q escapes ledger-api repository", rel)}, nil
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return StagePatchResult{Branch: branch, Error: err.Error()}, nil
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return StagePatchResult{Branch: branch, Error: err.Error()}, nil
		}
	}

	rep, verifyErr := ledgerverify.VerifyDir(repoDir)
	if verifyErr != nil {
		var findings []string
		if rep != nil {
			findings = rep.Findings
		}
		return StagePatchResult{
			Staged:         false,
			Branch:         branch,
			VerifyOK:       false,
			VerifyReport:   rep,
			VerifyFindings: findings,
			Error:          verifyErr.Error(),
		}, nil
	}

	gitBin, err := exec.LookPath("git")
	if err != nil {
		return StagePatchResult{}, fmt.Errorf("locate git: %w", err)
	}
	clearStaleGitLocks(repoDir)
	if _, err := runScopedGit(ctx, gitBin, repoDir, "checkout", "-B", branch); err != nil {
		return StagePatchResult{Branch: branch, Error: err.Error()}, nil
	}
	if _, err := runScopedGit(ctx, gitBin, repoDir, "add", "."); err != nil {
		return StagePatchResult{Branch: branch, Error: err.Error()}, nil
	}
	diffOut, _ := runScopedGit(ctx, gitBin, repoDir, "diff", "--cached")
	if strings.TrimSpace(diffOut) != "" {
		if _, err := runScopedGit(ctx, gitBin, repoDir, "commit", "-m", msg); err != nil {
			return StagePatchResult{Branch: branch, Error: err.Error()}, nil
		}
	}
	shaOut, _ := runScopedGit(ctx, gitBin, repoDir, "rev-parse", "HEAD")
	showDiff, _ := runScopedGit(ctx, gitBin, repoDir, "diff", "main..HEAD")
	if strings.TrimSpace(showDiff) == "" {
		showDiff = diffOut
	}

	return StagePatchResult{
		Staged:       true,
		Branch:       branch,
		CommitSHA:    strings.TrimSpace(shaOut),
		Diff:         strings.TrimSpace(showDiff),
		VerifyOK:     true,
		VerifyReport: rep,
	}, nil
}

// Inspect returns a point-in-time snapshot of /workspace/ledger-api without modifying files.
func (w *Workspace) Inspect(ctx context.Context) WorkspaceState {
	w.mu.Lock()
	defer w.mu.Unlock()

	state := WorkspaceState{WorkspaceDir: w.rootDir}
	repoDir, err := w.resolveInside(defaultRepoName, false)
	if err != nil {
		return state
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		return state
	}
	state.Cloned = true
	state.RepoDir = defaultRepoName

	if gitBin, err := exec.LookPath("git"); err == nil {
		branchOut, _ := runScopedGit(ctx, gitBin, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
		shaOut, _ := runScopedGit(ctx, gitBin, repoDir, "rev-parse", "--short", "HEAD")
		subjOut, _ := runScopedGit(ctx, gitBin, repoDir, "log", "-1", "--format=%s")
		statusOut, _ := runScopedGit(ctx, gitBin, repoDir, "status", "--short")
		diffOut, _ := runScopedGit(ctx, gitBin, repoDir, "diff", "main")
		if strings.TrimSpace(diffOut) == "" {
			diffOut, _ = runScopedGit(ctx, gitBin, repoDir, "diff")
		}
		state.Branch = strings.TrimSpace(branchOut)
		state.HeadSHA = strings.TrimSpace(shaOut)
		state.HeadSubject = strings.TrimSpace(subjOut)
		state.StatusShort = strings.TrimSpace(statusOut)
		state.Diff = strings.TrimSpace(diffOut)
	}

	rep, verifyErr := ledgerverify.VerifyDir(repoDir)
	state.VerifyReport = rep
	if rep != nil {
		state.VerifyOK = rep.OK && verifyErr == nil
		state.VerifyFindings = append([]string(nil), rep.Findings...)
	}
	return state
}

// NewWorkspaceTools builds the four ADK Go v2 functiontools backed by ws.
func NewWorkspaceTools(ws *Workspace, observer func(ToolStep)) ([]tool.Tool, error) {
	record := func(step ToolStep) {
		if observer != nil {
			observer(step)
		}
	}

	cloneTool, err := functiontool.New(functiontool.Config{
		Name:        "clone_repo",
		Description: "Clone or refresh the Target Service git repository (ledger-api) from the Operations Hub into /workspace/ledger-api.",
	}, func(ctx agent.Context, in CloneRepoArgs) (CloneRepoResult, error) {
		start := time.Now()
		res, err := ws.CloneRepo(ctx, in)
		record(makeToolStep("clone_repo", in, res, err, time.Since(start)))
		return res, err
	})
	if err != nil {
		return nil, err
	}

	readTool, err := functiontool.New(functiontool.Config{
		Name:        "read_workspace_file",
		Description: "Read a file or list a directory inside the Incident Actor's /workspace sandbox.",
	}, func(ctx agent.Context, in ReadFileArgs) (ReadFileResult, error) {
		start := time.Now()
		res, err := ws.ReadFile(ctx, in)
		record(makeToolStep("read_workspace_file", in, res, err, time.Since(start)))
		return res, err
	})
	if err != nil {
		return nil, err
	}

	cmdTool, err := functiontool.New(functiontool.Config{
		Name:        "run_workspace_command",
		Description: "Run an allowlisted verification or git inspection command (ledger-verify, git, or sh on unpacked .skills/ scripts) inside /workspace.",
	}, func(ctx agent.Context, in RunCommandArgs) (RunCommandResult, error) {
		start := time.Now()
		res, err := ws.RunCommand(ctx, in)
		record(makeToolStep("run_workspace_command", in, res, err, time.Since(start)))
		return res, err
	})
	if err != nil {
		return nil, err
	}

	stageTool, err := functiontool.New(functiontool.Config{
		Name:        "stage_git_patch",
		Description: "Write updated files into /workspace/ledger-api, verify invariants with ledger-verify, and stage a git commit on a remediation branch.",
	}, func(ctx agent.Context, in StagePatchArgs) (StagePatchResult, error) {
		start := time.Now()
		res, err := ws.StagePatch(ctx, in)
		record(makeToolStep("stage_git_patch", in, res, err, time.Since(start)))
		return res, err
	})
	if err != nil {
		return nil, err
	}

	return []tool.Tool{cloneTool, readTool, cmdTool, stageTool}, nil
}

func makeToolStep(name string, in any, out any, err error, dur time.Duration) ToolStep {
	argsJSON, _ := json.Marshal(in)
	var resStr string
	if err != nil {
		resStr = "error: " + err.Error()
	} else {
		resBytes, _ := json.Marshal(out)
		resStr = string(resBytes)
	}
	if len(resStr) > 2048 {
		resStr = resStr[:2048] + "...(truncated)"
	}
	return ToolStep{
		Name:       name,
		Args:       string(argsJSON),
		Result:     resStr,
		DurationMs: dur.Milliseconds(),
	}
}

func clearStaleGitLocks(repoDir string) {
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

func runScopedGit(ctx context.Context, gitBin, dir string, args ...string) (string, error) {
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

func listRepoRelativeFiles(repoDir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(repoDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			if rel, relErr := filepath.Rel(repoDir, p); relErr == nil {
				out = append(out, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// SeedInitialRepoForTest writes the initial unpatched ledger-api files into dir for tests.
func SeedInitialRepoForTest(dir string) error {
	return ledgerverify.WriteInitialRepoFiles(dir)
}
