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

// Package ledgerverify seeds the degraded ledger-api repository fixture and
// validates workspace fixes against configuration and AST invariants.
package ledgerverify

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	minMaxOpenConns       = 16
	maxMaxOpenConns       = 256
	minIdleTimeoutSeconds = 5
	maxIdleTimeoutSeconds = 300
	minAcquireTimeoutMs   = 50
	maxAcquireTimeoutMs   = 5000
	maxRepoFileBytes      = 1 << 20 // 1 MiB cap per inspected source file.
)

// PoolYAML represents the parsed contents of config/pool.yaml.
type PoolYAML struct {
	MaxOpenConns       int `json:"max_open_conns"`
	IdleTimeoutSeconds int `json:"idle_timeout_seconds"`
	AcquireTimeoutMs   int `json:"acquire_timeout_ms"`
}

// Report summarizes the verification result for a ledger-api checkout.
type Report struct {
	OK                 bool     `json:"ok"`
	MaxOpenConns       int      `json:"max_open_conns"`
	IdleTimeoutSeconds int      `json:"idle_timeout_seconds"`
	AcquireTimeoutMs   int      `json:"acquire_timeout_ms"`
	DeferClosePresent  bool     `json:"defer_close_present"`
	Findings           []string `json:"findings,omitempty"`
}

// WriteInitialRepoFiles populates dir with the initial degraded ledger-api
// repository contents served by ops-hub on startup.
func WriteInitialRepoFiles(dir string) error {
	files := map[string]string{
		"README.md":                            initialReadme,
		"go.mod":                               initialGoMod,
		filepath.Join("config", "pool.yaml"):   initialPoolYAML,
		filepath.Join("pool", "store.go"):      initialStoreGo,
		filepath.Join("pool", "store_test.go"): initialStoreTestGo,
	}
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}
	return nil
}

// ParsePoolYAML parses config/pool.yaml key-value settings.
func ParsePoolYAML(raw []byte) (PoolYAML, error) {
	var cfg PoolYAML
	seen := map[string]bool{}
	lines := strings.Split(string(raw), "\n")
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if i := strings.IndexByte(trimmed, '#'); i >= 0 {
			trimmed = strings.TrimSpace(trimmed[:i])
		}
		if trimmed == "" {
			continue
		}
		key, valStr, ok := strings.Cut(trimmed, ":")
		if !ok {
			return PoolYAML{}, fmt.Errorf("line %d: expected key: value", idx+1)
		}
		key = strings.TrimSpace(key)
		valStr = strings.TrimSpace(valStr)
		val, err := strconv.Atoi(valStr)
		if err != nil {
			return PoolYAML{}, fmt.Errorf("line %d (%s): invalid integer %q", idx+1, key, valStr)
		}
		switch key {
		case "max_open_conns":
			cfg.MaxOpenConns = val
		case "idle_timeout_seconds":
			cfg.IdleTimeoutSeconds = val
		case "acquire_timeout_ms":
			cfg.AcquireTimeoutMs = val
		default:
			return PoolYAML{}, fmt.Errorf("line %d: unknown setting %q", idx+1, key)
		}
		seen[key] = true
	}
	for _, req := range []string{"max_open_conns", "idle_timeout_seconds", "acquire_timeout_ms"} {
		if !seen[req] {
			return PoolYAML{}, fmt.Errorf("missing required key %q", req)
		}
	}
	return cfg, nil
}

// VerifyDir checks config/pool.yaml and pool/store.go inside dir.
func VerifyDir(dir string) (*Report, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("repository directory is empty")
	}
	absRoot, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return nil, fmt.Errorf("resolve root directory: %w", err)
	}
	evalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("evaluate root directory %s: %w", absRoot, err)
	}
	info, err := os.Stat(evalRoot)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("root path %s is not a directory", evalRoot)
	}

	poolBytes, err := readBoundedRepoFile(evalRoot, filepath.Join("config", "pool.yaml"))
	if err != nil {
		return nil, err
	}
	storeBytes, err := readBoundedRepoFile(evalRoot, filepath.Join("pool", "store.go"))
	if err != nil {
		return nil, err
	}

	rep := &Report{}
	cfg, err := ParsePoolYAML(poolBytes)
	if err != nil {
		rep.Findings = append(rep.Findings, fmt.Sprintf("config/pool.yaml: %v", err))
	} else {
		rep.MaxOpenConns = cfg.MaxOpenConns
		rep.IdleTimeoutSeconds = cfg.IdleTimeoutSeconds
		rep.AcquireTimeoutMs = cfg.AcquireTimeoutMs
		if cfg.MaxOpenConns < minMaxOpenConns || cfg.MaxOpenConns > maxMaxOpenConns {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"config/pool.yaml: max_open_conns is %d, want between %d and %d",
				cfg.MaxOpenConns, minMaxOpenConns, maxMaxOpenConns,
			))
		}
		if cfg.IdleTimeoutSeconds < minIdleTimeoutSeconds || cfg.IdleTimeoutSeconds > maxIdleTimeoutSeconds {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"config/pool.yaml: idle_timeout_seconds is %d, want between %d and %d",
				cfg.IdleTimeoutSeconds, minIdleTimeoutSeconds, maxIdleTimeoutSeconds,
			))
		}
		if cfg.AcquireTimeoutMs < minAcquireTimeoutMs || cfg.AcquireTimeoutMs > maxAcquireTimeoutMs {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"config/pool.yaml: acquire_timeout_ms is %d, want between %d and %d",
				cfg.AcquireTimeoutMs, minAcquireTimeoutMs, maxAcquireTimeoutMs,
			))
		}
	}

	deferOK, astErr := checkReconcileDeferClose(storeBytes)
	if astErr != nil {
		rep.Findings = append(rep.Findings, fmt.Sprintf("pool/store.go: %v", astErr))
	} else {
		rep.DeferClosePresent = deferOK
		if !deferOK {
			rep.Findings = append(rep.Findings,
				"pool/store.go: ReconcileTransfer must call defer rows.Close() before ValidateChecksum() so checksum errors release the pool slot",
			)
		}
	}

	if len(rep.Findings) > 0 {
		return rep, errors.New(strings.Join(rep.Findings, "; "))
	}
	rep.OK = true
	return rep, nil
}

func readBoundedRepoFile(rootDir, relPath string) ([]byte, error) {
	cleanedRel := filepath.Clean(relPath)
	if filepath.IsAbs(cleanedRel) || strings.HasPrefix(cleanedRel, "..") {
		return nil, fmt.Errorf("invalid relative path %q", relPath)
	}
	full := filepath.Join(rootDir, cleanedRel)
	prefix := rootDir + string(filepath.Separator)
	if !strings.HasPrefix(full, prefix) {
		return nil, fmt.Errorf("path %q escapes repository boundary", relPath)
	}

	cur := rootDir
	for _, part := range strings.Split(cleanedRel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.ToSlash(cleanedRel), err)
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s: symlink paths are not permitted", filepath.ToSlash(cleanedRel))
		}
	}
	st, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.ToSlash(cleanedRel), err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: expected regular file", filepath.ToSlash(cleanedRel))
	}
	if st.Size() > maxRepoFileBytes {
		return nil, fmt.Errorf("%s: file size %d exceeds %d byte limit", filepath.ToSlash(cleanedRel), st.Size(), maxRepoFileBytes)
	}
	return os.ReadFile(full)
}

func checkReconcileDeferClose(src []byte) (bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "pool/store.go", src, 0)
	if err != nil {
		return false, fmt.Errorf("parse error: %w", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "ReconcileTransfer" || fn.Body == nil {
			continue
		}
		rowsVar := "rows"
		sawAcquire := false
		sawDeferClose := false
		for _, stmt := range fn.Body.List {
			if assign, ok := stmt.(*ast.AssignStmt); ok && len(assign.Lhs) >= 1 && len(assign.Rhs) == 1 {
				if callContainsMethod(assign.Rhs[0], "AcquireRows") {
					if id, ok := assign.Lhs[0].(*ast.Ident); ok && id.Name != "" {
						rowsVar = id.Name
					}
					sawAcquire = true
					continue
				}
			}
			if def, ok := stmt.(*ast.DeferStmt); ok && sawAcquire {
				if isSelectorCall(def.Call, rowsVar, "Close") {
					sawDeferClose = true
					continue
				}
			}
			if stmtCallsMethod(stmt, "ValidateChecksum") {
				return sawAcquire && sawDeferClose, nil
			}
		}
		return sawAcquire && sawDeferClose, nil
	}
	return false, errors.New("missing function ReconcileTransfer")
}

func callContainsMethod(expr ast.Expr, method string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && sel.Sel.Name == method
}

func isSelectorCall(call *ast.CallExpr, recvName, method string) bool {
	if call == nil {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != method {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == recvName
}

func stmtCallsMethod(stmt ast.Stmt, method string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == method {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

const initialReadme = `# ledger-api

Production transfer reconciliation service running in namespace prod-sim.

## Service SLOs

- Availability: >= 99.5% HTTP 200 on /api/transfer and /healthz
- Latency: p99 < 150ms
- Pool configuration: config/pool.yaml
- Transfer reconciliation worker: pool/store.go
`

const initialGoMod = `module example.internal/ledger-api

go 1.27.0
`

const initialPoolYAML = `# Connection pool settings for ledger-api.
max_open_conns: 2
idle_timeout_seconds: 0
acquire_timeout_ms: 150
`

const initialStoreGo = `// Copyright 2026 Google LLC

package pool

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrPoolExhausted   = errors.New("ledger_db: connection pool exhausted")
	ErrInvalidChecksum = errors.New("ledger_db: transfer checksum mismatch")
)

type Config struct {
	MaxOpenConns       int
	IdleTimeoutSeconds int
	AcquireTimeout     time.Duration
}

type Store struct {
	slots  chan struct{}
	cfg    Config
	mu     sync.Mutex
	leaked int
}

func NewStore(cfg Config) *Store {
	if cfg.MaxOpenConns <= 0 {
		cfg.MaxOpenConns = 2
	}
	if cfg.AcquireTimeout <= 0 {
		cfg.AcquireTimeout = 150 * time.Millisecond
	}
	return &Store{
		slots: make(chan struct{}, cfg.MaxOpenConns),
		cfg:   cfg,
	}
}

type Rows struct {
	store  *Store
	txID   string
	closed bool
	mu     sync.Mutex
}

func (s *Store) AcquireRows(ctx context.Context, txID string) (*Rows, error) {
	timer := time.NewTimer(s.cfg.AcquireTimeout)
	defer timer.Stop()
	select {
	case s.slots <- struct{}{}:
		return &Rows{store: s, txID: txID}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, ErrPoolExhausted
	}
}

func (r *Rows) ValidateChecksum() error {
	if strings.HasPrefix(r.txID, "bad-") {
		return ErrInvalidChecksum
	}
	return nil
}

func (r *Rows) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	<-r.store.slots
	return nil
}

// ReconcileTransfer acquires a ledger row cursor, validates its checksum, and
// releases the connection slot back to the pool.
func (s *Store) ReconcileTransfer(ctx context.Context, txID string) error {
	rows, err := s.AcquireRows(ctx, txID)
	if err != nil {
		return err
	}
	if err := rows.ValidateChecksum(); err != nil {
		s.mu.Lock()
		s.leaked++
		s.mu.Unlock()
		return err
	}
	return rows.Close()
}
`

const initialStoreTestGo = `// Copyright 2026 Google LLC

package pool

import (
	"context"
	"testing"
	"time"
)

func TestReconcileTransferReleasesSlotOnError(t *testing.T) {
	s := NewStore(Config{
		MaxOpenConns:       2,
		IdleTimeoutSeconds: 30,
		AcquireTimeout:     50 * time.Millisecond,
	})
	for i := 0; i < 4; i++ {
		_ = s.ReconcileTransfer(context.Background(), "bad-checksum")
	}
	if err := s.ReconcileTransfer(context.Background(), "ok-tx-1"); err != nil {
		t.Fatalf("expected pool slot to remain available after checksum errors, got: %v", err)
	}
}
`
