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

// Package opshub implements the self-contained in-cluster Operations Hub:
// Smart HTTP Git repository, incident tracker, governed remediation PR & MCP
// server, and OAuth 2.0 Authorization Code + S256 PKCE provider.
package opshub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gke-demos/substrate-agent-platform/app/internal/ledgersvc"
	"github.com/gke-demos/substrate-agent-platform/app/internal/netutil"
)

const (
	strictCSP      = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; frame-ancestors 'none'"
	maxJSONBodyLen = 128 << 10
)

// Options configures an Operations Hub Server.
type Options struct {
	DataDir           string
	PublicBaseURL     string
	PublicGitURL      string
	SimNamespace      string
	LedgerAPIURL      string
	Require3LO        bool
	OAuthClientID     string
	OAuthClientSecret string
}

// Server hosts the Operations Hub endpoints and state.
type Server struct {
	dataDir           string
	bareRepoDir       string
	gitBin            string
	gitMu             sync.Mutex
	publicBaseURL     string
	publicGitURL      string
	simNamespace      string
	ledgerAPIURL      string
	oauthClientID     string
	oauthClientSecret string
	httpClient        *http.Client

	mu            sync.Mutex
	require3LO    bool
	incidents     map[string]*Incident
	incidentOrder []string
	pulls         []PullRequest
	activePoolCfg ledgersvc.RuntimeConfig
	authCodes     map[string]*authCodeRecord
	accessTokens  map[string]*accessTokenRecord

	limiter *netutil.RateLimiter
}

// NewServer initializes the bare ledger-api.git repository and returns a ready Server.
func NewServer(opts Options) (*Server, error) {
	dataDir := strings.TrimSpace(opts.DataDir)
	if dataDir == "" {
		var err error
		dataDir, err = os.MkdirTemp("", "opshub-*")
		if err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dataDir, err)
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("locate git: %w", err)
	}
	bareRepoDir, err := initBareLedgerRepo(dataDir)
	if err != nil {
		return nil, err
	}

	simNS := strings.TrimSpace(opts.SimNamespace)
	if simNS == "" {
		simNS = "prod-sim"
	}
	pubGit := strings.TrimSpace(opts.PublicGitURL)
	if pubGit == "" {
		pubGit = fmt.Sprintf("http://ops-hub.%s.svc.cluster.local:8080/git/ledger-api.git", simNS)
	}

	clientSecret := strings.TrimSpace(opts.OAuthClientSecret)
	if clientSecret == "" {
		clientSecret, err = randTokenHex(32)
		if err != nil {
			return nil, fmt.Errorf("generate ephemeral OAuth secret: %w", err)
		}
	}

	incList := defaultIncidents(pubGit, simNS)
	incMap := make(map[string]*Incident, len(incList))
	order := make([]string, 0, len(incList))
	for _, inc := range incList {
		incMap[inc.ID] = inc
		order = append(order, inc.ID)
	}

	s := &Server{
		dataDir:           dataDir,
		bareRepoDir:       bareRepoDir,
		gitBin:            gitBin,
		publicBaseURL:     strings.TrimRight(strings.TrimSpace(opts.PublicBaseURL), "/"),
		publicGitURL:      pubGit,
		simNamespace:      simNS,
		ledgerAPIURL:      strings.TrimRight(strings.TrimSpace(opts.LedgerAPIURL), "/"),
		oauthClientID:     strings.TrimSpace(opts.OAuthClientID),
		oauthClientSecret: clientSecret,
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		require3LO:        opts.Require3LO,
		incidents:         incMap,
		incidentOrder:     order,
		pulls:             make([]PullRequest, 0),
		activePoolCfg: ledgersvc.RuntimeConfig{
			MaxOpenConns:       2,
			IdleTimeoutSeconds: 0,
			AcquireTimeoutMs:   150,
			LeakFixed:          false,
		},
		authCodes:    make(map[string]*authCodeRecord),
		accessTokens: make(map[string]*accessTokenRecord),
		limiter:      netutil.NewRateLimiter(250, 500),
	}
	return s, nil
}

// Handler returns the secured HTTP handler for Operations Hub.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/git/", s.handleGitSmartHTTP)
	mux.HandleFunc("/api/incidents", s.handleIncidents)
	mux.HandleFunc("/api/incidents/", s.handleIncidentByID)
	mux.HandleFunc("/api/commits", s.handleCommits)
	mux.HandleFunc("/api/pulls", s.handlePulls)
	mux.HandleFunc("/api/config/pool", s.handlePoolConfig)
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.handleOAuthDiscovery)
	mux.HandleFunc("/oauth/consent.css", s.handleConsentCSS)
	mux.HandleFunc("/oauth/authorize", s.handleOAuthAuthorize)
	mux.HandleFunc("/oauth/token", s.handleOAuthToken)
	mux.HandleFunc("/oauth/userinfo", s.handleOAuthUserInfo)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		applySecurityHeaders(w, r.URL.Path)
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, HEAD, POST")
			writeJSONResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if !s.limiter.Allow(netutil.ClientIP(r.RemoteAddr)) {
			writeJSONResp(w, http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSONResp(w, http.StatusOK, map[string]string{"status": "ok", "service": "ops-hub"})
}

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s.mu.Lock()
	out := make([]Incident, 0, len(s.incidentOrder))
	for _, id := range s.incidentOrder {
		if inc, ok := s.incidents[id]; ok {
			cp := *inc
			out = append(out, cp)
		}
	}
	s.mu.Unlock()
	writeJSONResp(w, http.StatusOK, out)
}

func (s *Server) handleIncidentByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/incidents/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	inc, ok := s.getIncident(id)
	if !ok {
		writeJSONResp(w, http.StatusNotFound, map[string]string{"error": "incident not found"})
		return
	}
	writeJSONResp(w, http.StatusOK, inc)
}

func (s *Server) getIncident(id string) (Incident, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inc, ok := s.incidents[strings.ToUpper(strings.TrimSpace(id))]
	if !ok {
		return Incident{}, false
	}
	return *inc, true
}

func (s *Server) handleCommits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	branch := r.URL.Query().Get("branch")
	commits, err := s.listRepoCommits(r.Context(), branch, 10)
	if err != nil {
		writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSONResp(w, http.StatusOK, commits)
}

func (s *Server) handlePoolConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s.mu.Lock()
	cfg := s.activePoolCfg
	s.mu.Unlock()
	writeJSONResp(w, http.StatusOK, cfg)
}

type createPullRequest struct {
	IncidentID string `json:"incident_id"`
	Branch     string `json:"branch"`
	Title      string `json:"title"`
}

func (s *Server) handlePulls(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		s.mu.Lock()
		cp := make([]PullRequest, len(s.pulls))
		copy(cp, s.pulls)
		s.mu.Unlock()
		writeJSONResp(w, http.StatusOK, cp)
		return
	}
	if r.Method != http.MethodPost {
		writeJSONResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	operatorSub := ""
	if s.is3LORequired(r) || strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		tokRec, ok := s.authenticateBearer(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ops-hub", scope="remediation:write"`)
			writeJSONResp(w, http.StatusUnauthorized, map[string]string{"error": "3LO Bearer token required"})
			return
		}
		if !hasOAuthScope(tokRec.scope, "remediation:write") {
			writeJSONResp(w, http.StatusForbidden, map[string]string{"error": "missing scope remediation:write"})
			return
		}
		operatorSub = tokRec.operatorSub
	}

	var req createPullRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodyLen))
	if err := dec.Decode(&req); err != nil {
		writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "invalid pull request JSON"})
		return
	}
	agentID := strings.TrimSpace(r.Header.Get("X-Agent-Identity-Principal"))
	if agentID == "" {
		agentID = strings.TrimSpace(r.Header.Get("X-Agent-Identity"))
	}
	delegated := strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Agentic-Delegation")), "true")
	pr, statusCode, errMsg := s.applyRemediationPR(r.Context(), req.IncidentID, req.Branch, req.Title, operatorSub, agentID, delegated)
	if statusCode != http.StatusCreated {
		writeJSONResp(w, statusCode, map[string]string{"error": errMsg})
		return
	}
	writeJSONResp(w, http.StatusCreated, pr)
}

func (s *Server) applyRemediationPR(ctx context.Context, incidentID, branch, title, operatorSub, agentID string, delegated bool) (PullRequest, int, string) {
	incidentID = strings.ToUpper(strings.TrimSpace(incidentID))
	branch = strings.TrimSpace(branch)
	title = strings.TrimSpace(title)
	if incidentID == "" || branch == "" || title == "" {
		return PullRequest{}, http.StatusBadRequest, "incident_id, branch, and title are required"
	}
	if _, ok := s.getIncident(incidentID); !ok {
		return PullRequest{}, http.StatusNotFound, fmt.Sprintf("incident %s not found", incidentID)
	}

	rep, commitSHA, err := s.verifyAndMergeBranch(ctx, branch)
	if err != nil {
		return PullRequest{}, http.StatusUnprocessableEntity, fmt.Sprintf("remediation verification failed: %v", err)
	}

	nextCfg := ledgersvc.RuntimeConfig{
		MaxOpenConns:       rep.MaxOpenConns,
		IdleTimeoutSeconds: rep.IdleTimeoutSeconds,
		AcquireTimeoutMs:   rep.AcquireTimeoutMs,
		LeakFixed:          true,
	}

	s.mu.Lock()
	prID := fmt.Sprintf("PR-%d", len(s.pulls)+1)
	pr := PullRequest{
		ID:                prID,
		IncidentID:        incidentID,
		Branch:            branch,
		Title:             title,
		Status:            "MERGED",
		CommitSHA:         commitSHA,
		OperatorSub:       operatorSub,
		AgentIdentity:     agentID,
		AgenticDelegation: delegated || (operatorSub != "" && agentID != ""),
		CreatedAt:         time.Now().UTC().Format(time.RFC3339),
	}
	s.pulls = append(s.pulls, pr)
	s.activePoolCfg = nextCfg
	if inc, ok := s.incidents[incidentID]; ok {
		inc.Status = "RESOLVED"
		inc.ResolutionPRID = prID
		inc.MergedCommit = commitSHA
		inc.MergedBy = operatorSub
		inc.MergedAgent = agentID
	}
	s.mu.Unlock()

	s.notifyLedgerReload(ctx, nextCfg)
	return pr, http.StatusCreated, ""
}

func (s *Server) notifyLedgerReload(ctx context.Context, nextCfg ledgersvc.RuntimeConfig) {
	if s.ledgerAPIURL == "" {
		return
	}
	b, err := json.Marshal(nextCfg)
	if err != nil {
		return
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, s.ledgerAPIURL+"/api/config/reload", bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if reloadTok := strings.TrimSpace(os.Getenv("LEDGER_RELOAD_TOKEN")); reloadTok != "" {
		req.Header.Set("X-Ledger-Reload-Token", reloadTok)
	}
	resp, err := s.httpClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

func applySecurityHeaders(w http.ResponseWriter, reqPath string) {
	h := w.Header()
	h.Set("Content-Security-Policy", strictCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if strings.HasPrefix(reqPath, "/api/") || strings.HasPrefix(reqPath, "/oauth/") || strings.HasPrefix(reqPath, "/.well-known/") || reqPath == "/mcp" {
		h.Set("Cache-Control", "no-store")
		h.Set("Pragma", "no-cache")
	}
}

func writeJSONResp(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
