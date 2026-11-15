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

// Package broker implements sre-gateway, the always-on Governed Broker outside
// the Actor Boundary that serves the Google Cloud Console-modeled Incident
// Console, routes turns over mTLS to atenet-router, proxies keyless Gemini and
// Operations Hub calls, and suspends idle or gated Incident Actors to 0 vCPU.
package broker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/agentruntime"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ateclient"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/evals"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ledgersvc"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/netutil"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/opshub"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/telemetry"
	ateapipb "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc"
)

//go:embed web/*
var webAssets embed.FS

const (
	// TargetActorHeader routes HTTP requests through atenet-router to "<atespace>/<actor>".
	TargetActorHeader = "ate-target-actor"
	// TargetAtespaceHeader records the target atespace alongside ate-target-actor.
	TargetAtespaceHeader = "ate-target-atespace"

	csrfHostCookieName = "__Host-sre_csrf"
	csrfCookieName     = "sre_csrf"
	themeCookieName    = "sre_theme"
	csrfHeaderName     = "X-CSRF-Token"

	consoleCSP = "default-src 'self'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"
	maxAPIBody = 256 << 10
)

var validActorLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// Config configures the Governed Broker (sre-gateway).
type Config struct {
	ListenAddr               string
	Namespace                string
	Atespace                 string
	SimNamespace             string
	ProjectID                string
	Region                   string
	ClusterName              string
	ClusterLocation          string
	ModelLocation            string
	GeminiModel              string
	MockLLM                  bool
	ReasoningEngineID        string
	DefaultTemplate          string
	SelfURL                  string
	RouterURL                string
	RouterServerName         string
	RouterCAFile             string
	RouterClientCert         string
	RouterInsecure           bool
	OpsHubURL                string
	LedgerAPIURL             string
	GenAIUpstreamURL         string
	AgentEngineUpstreamURL   string
	SkillRegistryLocation    string
	SkillRegistryUpstreamURL string
	AgentRegistryUpstreamURL string
	ModelArmorUpstreamURL    string
	ModelArmorTemplateID     string
	GovernanceEnforced       bool
	Require3LO               bool
	GCPAuthProviderID        string
	AuthIdentityUpstreamURL  string
	OAuthClientID            string
	OAuthClientSecret        string
	GitHubOAuthClientID      string
	GitHubOAuthClientSecret  string
	LoggingUpstreamURL       string
	GKEUpstreamURL           string
	CloudTraceUpstreamURL    string
	StateFile                string
	AdminToken               string
	IdleAfter                time.Duration
	IdleCheckEvery           time.Duration
	SPIFFEMode               string
	SPIFFEDomain             string
	AgentIdentityCertFile    string
	ATE                      ateclient.Options
}

// ChatRequest is posted by the Incident Console or CLI to POST /api/chat.
type ChatRequest struct {
	IncidentID            string `json:"incident_id"`
	SessionID             string `json:"session_id,omitempty"`
	Prompt                string `json:"prompt"`
	Template              string `json:"template,omitempty"`
	ConfirmationCallID    string `json:"confirmation_call_id,omitempty"`
	ConfirmationConfirmed *bool  `json:"confirmation_confirmed,omitempty"`
	gateApprovalToken     string
}

// ChatResponse returns the Incident Actor's turn result and workspace state.
type ChatResponse struct {
	IncidentID             string                      `json:"incident_id"`
	ActorName              string                      `json:"actor_name"`
	Atespace               string                      `json:"atespace"`
	SessionID              string                      `json:"session_id"`
	Template               string                      `json:"template"`
	Reply                  string                      `json:"reply"`
	Backend                string                      `json:"backend"`
	StarterStub            bool                        `json:"starter_stub,omitempty"`
	ToolSteps              []agentruntime.ToolStep     `json:"tool_steps"`
	Workspace              agentruntime.WorkspaceState `json:"workspace"`
	RemediationGatePending bool                        `json:"remediation_gate_pending"`
	ConfirmationCallID     string                      `json:"confirmation_call_id,omitempty"`
	CallID                 string                      `json:"call_id,omitempty"`
	ToolName               string                      `json:"tool_name,omitempty"`
	ToolArgs               map[string]any              `json:"args,omitempty"`
	ConfirmationHint       string                      `json:"hint,omitempty"`
	TraceID                string                      `json:"trace_id,omitempty"`
	ResumeDurationMs       int64                       `json:"resume_duration_ms,omitempty"`
	DurationMs             int64                       `json:"duration_ms"`
}

// ActorSummary describes an Incident Actor's live Substrate control-plane state.
type ActorSummary struct {
	Name         string `json:"name"`
	Atespace     string `json:"atespace"`
	Template     string `json:"template"`
	State        string `json:"state"`
	WorkerPod    string `json:"worker_pod,omitempty"`
	IdleSeconds  int64  `json:"idle_seconds"`
	ComputeBadge string `json:"compute_badge"`
}

// IncidentOverview pairs an Operations Hub ticket with its mapped Actor state.
type IncidentOverview struct {
	opshub.Incident
	ActorName    string              `json:"actor_name"`
	ActorState   string              `json:"actor_state"`
	ComputeBadge string              `json:"compute_badge"`
	Template     string              `json:"template,omitempty"`
	WorkerPod    string              `json:"worker_pod,omitempty"`
	PendingGate  *RemediationGateDTO `json:"pending_gate,omitempty"`
}

// PolicyEvent records an Actor Boundary governance decision surfaced in the UI.
type PolicyEvent struct {
	Timestamp string `json:"timestamp"`
	Boundary  string `json:"boundary"`
	Action    string `json:"action"`
	Verdict   string `json:"verdict"`
	Detail    string `json:"detail"`
}

// actorControl is the narrow ateapi surface used by Server.
type actorControl interface {
	GetActor(ctx context.Context, in *ateapipb.GetActorRequest, opts ...grpc.CallOption) (*ateapipb.Actor, error)
	CreateActor(ctx context.Context, in *ateapipb.CreateActorRequest, opts ...grpc.CallOption) (*ateapipb.Actor, error)
	DeleteActor(ctx context.Context, in *ateapipb.DeleteActorRequest, opts ...grpc.CallOption) (*ateapipb.Actor, error)
	ListActors(ctx context.Context, in *ateapipb.ListActorsRequest, opts ...grpc.CallOption) (*ateapipb.ListActorsResponse, error)
	SuspendActor(ctx context.Context, in *ateapipb.SuspendActorRequest, opts ...grpc.CallOption) (*ateapipb.SuspendActorResponse, error)
	GetActorEgressPolicy(ctx context.Context, in *ateapipb.GetActorEgressPolicyRequest, opts ...grpc.CallOption) (*ateapipb.EgressPolicy, error)
	CreateActorEgressPolicy(ctx context.Context, in *ateapipb.CreateActorEgressPolicyRequest, opts ...grpc.CallOption) (*ateapipb.EgressPolicy, error)
	UpdateActorEgressPolicy(ctx context.Context, in *ateapipb.UpdateActorEgressPolicyRequest, opts ...grpc.CallOption) (*ateapipb.EgressPolicy, error)
}

// Server hosts the Governed Broker HTTP handlers, ateapi client, and idle-suspender.
type Server struct {
	cfg           Config
	ate           actorControl
	ateCloser     io.Closer
	routerClient  *http.Client
	opsClient     *http.Client
	tracker       *IdleTracker
	suspender     *IdleSuspender
	genai         *genaiProxy
	agentEngine   *agentEngineProxy
	skillRegistry *skillRegistryProxy
	agentRegistry *agentRegistryProxy
	modelArmor    *modelArmorGuard
	authManager   *authManager
	identity      *identityInspector
	traces        *telemetry.Store
	metrics       *telemetry.MetricsRegistry
	evaluator     *evals.Evaluator
	limiter       *netutil.RateLimiter
	indexHTML     string
	subFS         fs.FS

	bgCancel context.CancelFunc

	mcpAllowedCount      atomic.Int64
	mcpDeniedCount       atomic.Int64
	remediationSuspended atomic.Bool

	stateWriteMu       sync.Mutex
	mu                 sync.Mutex
	workspaceCache     map[string]agentruntime.WorkspaceState
	activeTraceByActor map[string]telemetry.SpanContext
	latestEvalReport   *evals.Report
	policyEvents       []PolicyEvent
	a2aTasks           map[string]A2ATaskRecord
	pendingGates       map[string]RemediationGateDTO
	approvedGates      map[string]approvedGateTicket
}

// NewServer initializes sre-gateway components, mTLS router transport, and idle-suspender.
func NewServer(cfg Config) (*Server, error) {
	cfg.Namespace = firstNonEmpty(cfg.Namespace, "workshop-301")
	cfg.Atespace = firstNonEmpty(cfg.Atespace, cfg.Namespace)
	cfg.SimNamespace = firstNonEmpty(cfg.SimNamespace, "prod-sim")
	cfg.ModelLocation = firstNonEmpty(cfg.ModelLocation, "global")
	cfg.GeminiModel = firstNonEmpty(cfg.GeminiModel, "gemini-3.8-flash")
	cfg.DefaultTemplate = firstNonEmpty(cfg.DefaultTemplate, "sre-agent-v1")
	cfg.RouterURL = strings.TrimRight(firstNonEmpty(cfg.RouterURL, "https://atenet-router.ate-system.svc.cluster.local"), "/")
	cfg.RouterServerName = firstNonEmpty(cfg.RouterServerName, "atenet-router.ate-system.svc")
	cfg.OpsHubURL = strings.TrimRight(firstNonEmpty(cfg.OpsHubURL, fmt.Sprintf("http://ops-hub.%s.svc.cluster.local:8080", cfg.SimNamespace)), "/")
	cfg.LedgerAPIURL = strings.TrimRight(firstNonEmpty(cfg.LedgerAPIURL, fmt.Sprintf("http://ledger-api.%s.svc.cluster.local:8080", cfg.SimNamespace)), "/")
	cfg.SPIFFEMode = firstNonEmpty(cfg.SPIFFEMode, "wif")
	if cfg.SPIFFEDomain == "" {
		if cfg.ProjectID != "" {
			cfg.SPIFFEDomain = cfg.ProjectID + ".svc.id.goog"
		} else {
			cfg.SPIFFEDomain = "cluster.local"
		}
	}
	if cfg.IdleCheckEvery <= 0 {
		cfg.IdleCheckEvery = 2 * time.Second
	}

	var (
		ateClient   actorControl
		suspControl suspendClient
		ateCloser   io.Closer
		err         error
	)
	if strings.TrimSpace(cfg.ATE.Addr) != "" {
		rawATE, closer, dialErr := ateclient.Dial(cfg.ATE)
		if dialErr != nil {
			return nil, fmt.Errorf("dial ateapi: %w", dialErr)
		}
		ateClient = rawATE
		suspControl = rawATE
		ateCloser = closer
	}

	routerHC, err := buildRouterHTTPClient(cfg)
	if err != nil {
		if ateCloser != nil {
			_ = ateCloser.Close()
		}
		return nil, err
	}

	sub, err := fs.Sub(webAssets, "web")
	if err != nil {
		if ateCloser != nil {
			_ = ateCloser.Close()
		}
		return nil, fmt.Errorf("load embedded web assets: %w", err)
	}
	rawIndex, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		if ateCloser != nil {
			_ = ateCloser.Close()
		}
		return nil, fmt.Errorf("read index.html: %w", err)
	}

	tracker := NewIdleTracker(nil)
	metricsReg := telemetry.NewMetricsRegistry()
	traceStore := telemetry.NewStore(context.Background(), telemetry.StoreConfig{
		ProjectID:    cfg.ProjectID,
		Region:       cfg.Region,
		Atespace:     cfg.Atespace,
		DefaultModel: cfg.GeminiModel,
		MockLLM:      cfg.MockLLM,
		UpstreamURL:  cfg.CloudTraceUpstreamURL,
		ExtraSecrets: []string{cfg.OAuthClientSecret, cfg.GitHubOAuthClientSecret, cfg.AdminToken},
		Metrics:      metricsReg,
	})

	s := &Server{
		cfg:                cfg,
		ate:                ateClient,
		ateCloser:          ateCloser,
		routerClient:       routerHC,
		opsClient:          &http.Client{Timeout: 15 * time.Second},
		tracker:            tracker,
		traces:             traceStore,
		metrics:            metricsReg,
		limiter:            netutil.NewRateLimiter(300, 600),
		indexHTML:          string(rawIndex),
		subFS:              sub,
		workspaceCache:     make(map[string]agentruntime.WorkspaceState),
		activeTraceByActor: make(map[string]telemetry.SpanContext),
		a2aTasks:           make(map[string]A2ATaskRecord),
		pendingGates:       make(map[string]RemediationGateDTO),
		approvedGates:      make(map[string]approvedGateTicket),
		identity:           newIdentityInspector(cfg),
	}
	idSnap := s.identity.Inspect()
	s.recordPolicyEvent(PolicyEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Boundary:  "Actor Boundary",
		Action:    "Broker startup",
		Verdict:   "READY",
		Detail:    fmt.Sprintf("Governed Broker active in %s (SPIFFE=%s, mode=%s, svid_mounted=%t, idle_after=%s, mock_llm=%t)", cfg.Namespace, idSnap.TrustDomain, idSnap.Mode, idSnap.SVIDMounted, cfg.IdleAfter, cfg.MockLLM),
	})

	gp, err := newGenAIProxy(context.Background(), s, cfg, s.recordPolicyEvent)
	if err != nil {
		if ateCloser != nil {
			_ = ateCloser.Close()
		}
		return nil, err
	}
	s.genai = gp

	ae, err := newAgentEngineProxy(context.Background(), cfg, s, s.recordPolicyEvent)
	if err != nil {
		if ateCloser != nil {
			_ = ateCloser.Close()
		}
		return nil, err
	}
	s.agentEngine = ae

	sr, err := newSkillRegistryProxy(context.Background(), cfg, s, s.recordPolicyEvent)
	if err != nil {
		if ateCloser != nil {
			_ = ateCloser.Close()
		}
		return nil, err
	}
	s.skillRegistry = sr
	s.loadPersistedState()

	ar, err := newAgentRegistryProxy(context.Background(), s, cfg, s.recordPolicyEvent)
	if err != nil {
		if ateCloser != nil {
			_ = ateCloser.Close()
		}
		return nil, err
	}
	s.agentRegistry = ar
	s.modelArmor = newModelArmorGuard(context.Background(), s, cfg, s.recordPolicyEvent)
	am, err := newAuthManager(context.Background(), cfg, s.recordPolicyEvent)
	if err != nil {
		if ateCloser != nil {
			_ = ateCloser.Close()
		}
		return nil, err
	}
	s.authManager = am

	ev, err := evals.NewEvaluator(context.Background(), evals.Config{
		ProjectID:   cfg.ProjectID,
		Location:    cfg.ModelLocation,
		Model:       cfg.GeminiModel,
		MockLLM:     cfg.MockLLM,
		UpstreamURL: cfg.GenAIUpstreamURL,
	})
	if err != nil {
		if ateCloser != nil {
			_ = ateCloser.Close()
		}
		return nil, err
	}
	s.evaluator = ev

	s.suspender = &IdleSuspender{
		Control:   suspControl,
		Atespace:  cfg.Atespace,
		IdleAfter: cfg.IdleAfter,
		Tracker:   tracker,
		OnSuspend: func(actorName, reason string) {
			_, _, finishSusp := s.startBrokerSpan(context.Background(), "substrate.suspend_actor", "CLIENT", "Lifecycle & GCS Snapshot", map[string]any{
				"ate.actor.id":     actorName,
				"ate.atespace":     cfg.Atespace,
				"substrate.reason": reason,
			})
			finishSusp("OK", "", nil)
			s.recordPolicyEvent(PolicyEvent{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Boundary:  "Lifecycle & GCS Snapshot",
				Action:    "SuspendActor (" + actorName + ")",
				Verdict:   "SUSPENDED",
				Detail:    reason,
			})
		},
	}

	bgCtx, cancel := context.WithCancel(context.Background())
	s.bgCancel = cancel
	if ae.autoBootstrap {
		go ae.bootstrapReasoningEngine(bgCtx)
	}
	if ateClient != nil && cfg.IdleAfter > 0 {
		go s.suspender.Run(bgCtx, cfg.IdleCheckEvery)
	}

	return s, nil
}

func (s *Server) onBrokerSessionDeleted(sessID string) {
	incID := strings.ToUpper(strings.TrimSpace(sessID))
	if incID == "INC-402" {
		s.remediationSuspended.Store(false)
	}
	s.mu.Lock()
	_, existed := s.pendingGates[incID]
	delete(s.pendingGates, incID)
	for k, g := range s.pendingGates {
		if g.SessionID == sessID {
			delete(s.pendingGates, k)
			existed = true
		}
	}
	s.mu.Unlock()
	if existed {
		s.savePersistedState()
	}
}

func buildRouterHTTPClient(cfg Config) (*http.Client, error) {
	if !strings.HasPrefix(cfg.RouterURL, "https://") {
		return &http.Client{Timeout: 360 * time.Second}, nil
	}
	caFile := firstNonEmpty(cfg.RouterCAFile, cfg.ATE.CAFile)
	tc, err := ateclient.TLSConfig(ateclient.Options{
		ServerName:     cfg.RouterServerName,
		CAFile:         caFile,
		ClientCertFile: cfg.RouterClientCert,
		Insecure:       cfg.RouterInsecure || cfg.ATE.Insecure,
	})
	if err != nil {
		return nil, fmt.Errorf("configure atenet-router mTLS: %w", err)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tc
	tr.ForceAttemptHTTP2 = true
	return &http.Client{
		Transport: tr,
		Timeout:   360 * time.Second,
	}, nil
}

// Close stops background idle checks and closes the gRPC connection to ateapi.
func (s *Server) Close() error {
	if s.bgCancel != nil {
		s.bgCancel()
	}
	if s.ateCloser != nil {
		return s.ateCloser.Close()
	}
	return nil
}

// RunIdleTick runs one immediate idle-suspender pass (used in deterministic tests).
func (s *Server) RunIdleTick(ctx context.Context) error {
	return s.suspender.Tick(ctx)
}

func (s *Server) recordPolicyEvent(ev PolicyEvent) {
	if ev.Verdict == "SUSPENDED" && (ev.Boundary == "Remediation Gate" || strings.Contains(ev.Action, "inc-402")) {
		s.remediationSuspended.Store(true)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policyEvents = append([]PolicyEvent{ev}, s.policyEvents...)
	if len(s.policyEvents) > 100 {
		s.policyEvents = s.policyEvents[:100]
	}
}

// Handler returns the secured HTTP mux serving UI routes, operator APIs, and broker proxies.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/healthz", s.handleReadyz)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/otlp/v1/traces", s.handleOTLPTraces)

	// A2A Agent Card and JSON-RPC 2.0 endpoint for GKE Agent Registry discovery.
	mux.HandleFunc("/.well-known/agent-card.json", s.handleAgentCard)
	mux.HandleFunc("/a2a", s.handleA2A)

	// Keyless Actor outbound reverse-proxy endpoints across the Actor Boundary.
	mux.Handle("/genai/", s.genai)
	mux.Handle("/agentengine/", s.agentEngine)
	mux.Handle("/agentregistry/skills", s.skillRegistry)
	mux.Handle("/agentregistry/skills/", s.skillRegistry)
	mux.Handle("/agentregistry/v1/", s.agentRegistry)
	mux.HandleFunc("/mcp/", s.handleMCPBroker)
	mux.HandleFunc("/ops/", s.handleOpsProxy)

	// Operator & Incident Console REST endpoints + 3LO OAuth brokering.
	mux.HandleFunc("/oauth/authorize", s.handleOpsHubConsentProxy)
	mux.HandleFunc("/oauth/consent.css", s.handleOpsHubConsentProxy)
	mux.HandleFunc("/api/oauth/start", s.handleOAuthStart)
	mux.HandleFunc("/api/oauth/callback", s.handleOAuthCallback)
	mux.HandleFunc("/api/oauth/revoke", s.handleOAuthRevoke)
	mux.HandleFunc("/api/session", s.handleSessionBootstrap)
	mux.HandleFunc("/api/preferences", s.handlePreferences)
	mux.HandleFunc("/api/incidents", s.handleIncidents)
	mux.HandleFunc("/api/incidents/", s.handleIncidentSubroutes)
	mux.HandleFunc("/api/actors", s.handleActors)
	mux.HandleFunc("/api/actors/", s.handleActorSubroutes)
	mux.HandleFunc("/api/ledger/sessions", s.handleLedgerSessions)
	mux.HandleFunc("/api/ledger/sessions/", s.handleLedgerSessionDetail)
	mux.HandleFunc("/api/ledger/memories", s.handleLedgerMemories)
	mux.HandleFunc("/api/catalog", s.handleCatalog)
	mux.HandleFunc("/api/catalog/skills", s.handleCatalogSkills)
	mux.HandleFunc("/api/chat", s.handleChat)
	mux.HandleFunc("/api/governance", s.handleGovernance)
	mux.HandleFunc("/api/traces", s.handleTraces)
	mux.HandleFunc("/api/traces/", s.handleTraceSubroutes)
	mux.HandleFunc("/api/evals", s.handleEvals)
	mux.HandleFunc("/api/evals/run", s.handleRunEvals)
	mux.HandleFunc("/api/architecture", s.handleArchitecture)

	// Static assets and path-based Cloud Console SPA routes.
	fileServer := http.FileServer(http.FS(s.subFS))
	mux.Handle("/assets/", http.StripPrefix("/assets/", fileServer))
	mux.HandleFunc("/", s.handleSPAShell)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isAPIOrProxy := strings.HasPrefix(r.URL.Path, "/api/") ||
			strings.HasPrefix(r.URL.Path, "/oauth/") ||
			strings.HasPrefix(r.URL.Path, "/otlp/") ||
			r.URL.Path == "/metrics" ||
			r.URL.Path == "/.well-known/agent-card.json" ||
			r.URL.Path == "/a2a" ||
			strings.HasPrefix(r.URL.Path, "/mcp/") ||
			strings.HasPrefix(r.URL.Path, "/genai/") ||
			strings.HasPrefix(r.URL.Path, "/agentengine/") ||
			strings.HasPrefix(r.URL.Path, "/agentregistry/") ||
			strings.HasPrefix(r.URL.Path, "/ops/")
		setConsoleSecurityHeaders(w, isAPIOrProxy)

		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, HEAD, POST")
			writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if !s.limiter.Allow(netutil.ClientIP(r.RemoteAddr)) {
			writeBrokerJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded"})
			return
		}
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/") {
			if err := verifyOriginAndCSRF(r); err != nil {
				writeBrokerJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	idSnap := s.identity.Inspect()
	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"status":                      "ready",
		"service":                     "sre-gateway",
		"atespace":                    s.cfg.Atespace,
		"namespace":                   s.cfg.Namespace,
		"mock_llm":                    s.cfg.MockLLM,
		"gemini_model":                s.cfg.GeminiModel,
		"default_template":            s.cfg.DefaultTemplate,
		"idle_after":                  s.cfg.IdleAfter.String(),
		"spiffe_mode":                 idSnap.Mode,
		"spiffe_domain":               idSnap.TrustDomain,
		"spiffe_principal":            idSnap.Principal,
		"svid_mounted":                idSnap.SVIDMounted,
		"governance_enforced":         s.cfg.GovernanceEnforced,
		"require_3lo":                 s.cfg.Require3LO,
		"session_backend":             s.agentEngine.backendLabel(),
		"reasoning_engine_configured": s.agentEngine.cloudEnabled(),
	})
}

func (s *Server) handleSessionBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	isHTTPS := isHTTPSRequest(r)
	tok := readCSRFCookieValue(r)
	if len(tok) < 32 {
		var err error
		tok, err = randomHex(32)
		if err != nil {
			writeBrokerJSON(w, http.StatusInternalServerError, map[string]string{"error": "generate CSRF token failed"})
			return
		}
		cookieName := csrfCookieName
		if isHTTPS {
			cookieName = csrfHostCookieName
		}
		// TODO(security): Plain HTTP localhost port-forwarding during classroom labs cannot set
		// Secure/__Host- cookies; HTTPS ingress issues __Host-sre_csrf with Secure=true.
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    tok,
			Path:     "/",
			HttpOnly: true,
			Secure:   isHTTPS,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   3600,
		})
	}
	theme := readCookieValue(r, themeCookieName)
	if theme != "dark" {
		theme = "light"
	}
	idSnap := s.identity.Inspect()
	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"csrf_token":                  tok,
		"theme":                       theme,
		"project_id":                  firstNonEmpty(s.cfg.ProjectID, "local-dev"),
		"namespace":                   s.cfg.Namespace,
		"atespace":                    s.cfg.Atespace,
		"sim_namespace":               s.cfg.SimNamespace,
		"gemini_model":                s.cfg.GeminiModel,
		"mock_llm":                    s.cfg.MockLLM,
		"default_template":            s.cfg.DefaultTemplate,
		"idle_after":                  s.cfg.IdleAfter.String(),
		"spiffe_mode":                 idSnap.Mode,
		"spiffe_domain":               idSnap.TrustDomain,
		"spiffe_principal":            idSnap.Principal,
		"svid_mounted":                idSnap.SVIDMounted,
		"governance_enforced":         s.cfg.GovernanceEnforced,
		"require_3lo":                 s.cfg.Require3LO,
		"session_backend":             s.agentEngine.backendLabel(),
		"reasoning_engine_configured": s.agentEngine.cloudEnabled(),
	})
}

func (s *Server) handlePreferences(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var body struct {
		Theme string `json:"theme"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid preferences payload"})
		return
	}
	theme := "light"
	if strings.EqualFold(strings.TrimSpace(body.Theme), "dark") {
		theme = "dark"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     themeCookieName,
		Value:    theme,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPSRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400 * 30,
	})
	writeBrokerJSON(w, http.StatusOK, map[string]string{"theme": theme})
}

func (s *Server) handleSPAShell(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/", "/sessions", "/catalog", "/about":
	default:
		http.NotFound(w, r)
		return
	}
	htmlDoc := s.indexHTML
	if readCookieValue(r, themeCookieName) == "dark" {
		htmlDoc = strings.Replace(htmlDoc, `data-theme="light"`, `data-theme="dark"`, 1)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, htmlDoc)
}

func (s *Server) handleArchitecture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	idSnap := s.identity.Inspect()
	cat := BuildArchitectureCatalog(CatalogParams{
		Namespace:            s.cfg.Namespace,
		Atespace:             s.cfg.Atespace,
		SimNamespace:         s.cfg.SimNamespace,
		GeminiModel:          s.cfg.GeminiModel,
		DefaultTemplate:      s.cfg.DefaultTemplate,
		IdleAfter:            s.cfg.IdleAfter,
		SPIFFEMode:           idSnap.Mode,
		SPIFFEDomain:         idSnap.TrustDomain,
		SPIFFEPrincipal:      idSnap.Principal,
		SVIDMounted:          idSnap.SVIDMounted,
		GovernanceEnforced:   s.cfg.GovernanceEnforced,
		ModelArmorTemplateID: s.cfg.ModelArmorTemplateID,
		Require3LO:           s.cfg.Require3LO,
		GCPAuthProviderID:    s.cfg.GCPAuthProviderID,
	})
	writeBrokerJSON(w, http.StatusOK, cat)
}

func (s *Server) handleGovernance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	idSnap := s.identity.Inspect()
	oauthSnap := s.authManager.Status(default3LOOperator)
	recentPulls := s.fetchPullsFromOpsHub(r.Context())
	s.mu.Lock()
	events := append([]PolicyEvent(nil), s.policyEvents...)
	s.mu.Unlock()
	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"events":              events,
		"egress_host":         fmt.Sprintf("sre-gateway.%s.svc.cluster.local", s.cfg.Namespace),
		"spiffe_mode":         idSnap.Mode,
		"spiffe_domain":       idSnap.TrustDomain,
		"identity":            idSnap,
		"governance_enforced": s.cfg.GovernanceEnforced,
		"model_armor":         s.modelArmor.Status(),
		"mcp_policy": MCPPolicyStatus{
			Enforced:     true,
			AllowedCount: s.mcpAllowedCount.Load(),
			DeniedCount:  s.mcpDeniedCount.Load(),
		},
		"oauth_3lo":     oauthSnap,
		"oauth":         oauthSnap,
		"recent_pulls":  recentPulls,
		"keyless_actor": true,
	})
}

func (s *Server) actorSummariesMap(ctx context.Context) map[string]ActorSummary {
	actorsByName := make(map[string]ActorSummary)
	if list, err := s.listActorSummaries(ctx); err == nil {
		for _, a := range list {
			actorsByName[a.Name] = a
		}
	}
	return actorsByName
}

func (s *Server) handleLedgerSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	actorsByName := s.actorSummariesMap(r.Context())
	sessions := s.agentEngine.ListLedgerSessions(actorsByName, s.cfg.DefaultTemplate)
	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"backend":   s.agentEngine.backendLabel(),
		"zero_wake": true,
		"sessions":  sessions,
	})
}

func (s *Server) handleLedgerSessionDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sessID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/ledger/sessions/"))
	if sessID == "" || strings.Contains(sessID, "/") {
		http.NotFound(w, r)
		return
	}
	actorsByName := s.actorSummariesMap(r.Context())
	detail, ok := s.agentEngine.GetLedgerSessionDetail(sessID, actorsByName, s.cfg.DefaultTemplate)
	if !ok {
		writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("session %q not found in Session Ledger", sessID)})
		return
	}
	writeBrokerJSON(w, http.StatusOK, detail)
}

func (s *Server) handleLedgerMemories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	memories := s.agentEngine.searchMemories(r.Context(), agentruntime.AppName, userID, query)
	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"backend":  s.agentEngine.backendLabel(),
		"query":    query,
		"memories": memories,
	})
}

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	incidents := s.fetchIncidentsFromOpsHub(r.Context())
	actorsByName := map[string]ActorSummary{}
	if list, err := s.listActorSummaries(r.Context()); err == nil {
		for _, a := range list {
			actorsByName[a.Name] = a
		}
	}

	s.mu.Lock()
	gatesSnapshot := make(map[string]RemediationGateDTO, len(s.pendingGates))
	for k, v := range s.pendingGates {
		gatesSnapshot[k] = v
	}
	s.mu.Unlock()

	out := make([]IncidentOverview, 0, len(incidents))
	for _, inc := range incidents {
		actorName := incidentToActorName(inc.ID)
		summary, exists := actorsByName[actorName]
		state := "NOT_CREATED"
		badge := "On demand · Golden Snapshot"
		tmpl := s.cfg.DefaultTemplate
		worker := ""
		if exists {
			state = summary.State
			badge = summary.ComputeBadge
			tmpl = firstNonEmpty(summary.Template, tmpl)
			worker = summary.WorkerPod
		}
		var gatePtr *RemediationGateDTO
		if g, ok := gatesSnapshot[strings.ToUpper(inc.ID)]; ok {
			cp := g
			cp.ActorSuspended = state == "SUSPENDED" || cp.ActorSuspended
			gatePtr = &cp
		}
		out = append(out, IncidentOverview{
			Incident:     inc,
			ActorName:    actorName,
			ActorState:   state,
			ComputeBadge: badge,
			Template:     tmpl,
			WorkerPod:    worker,
			PendingGate:  gatePtr,
		})
	}

	var ledgerSnap *ledgersvc.StatusSnapshot
	if snap, err := s.fetchLedgerStatus(r.Context()); err == nil {
		ledgerSnap = snap
	}

	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"incidents":     out,
		"pending_gates": gatesSnapshot,
		"target_status": ledgerSnap,
	})
}

func (s *Server) handleActors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	list, err := s.listActorSummaries(r.Context())
	if err != nil {
		writeBrokerJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeBrokerJSON(w, http.StatusOK, list)
}

func (s *Server) handleActorSubroutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/actors/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || !validActorLabel.MatchString(parts[0]) {
		http.NotFound(w, r)
		return
	}
	actorName, action := parts[0], parts[1]

	switch action {
	case "suspend":
		if r.Method != http.MethodPost {
			writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if s.ate == nil {
			writeBrokerJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ateapi client not configured"})
			return
		}
		suspCtx, _, finishSusp := s.startBrokerSpan(r.Context(), "substrate.suspend_actor", "CLIENT", "Lifecycle & GCS Snapshot", map[string]any{
			"ate.actor.id":     actorName,
			"ate.atespace":     s.cfg.Atespace,
			"substrate.reason": "manual",
		})
		if err := ateclient.SuspendActor(suspCtx, s.ate, s.cfg.Atespace, actorName); err != nil {
			finishSusp("ERROR", err.Error(), nil)
			writeBrokerJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		finishSusp("OK", "", nil)
		s.tracker.Forget(actorName)
		s.recordPolicyEvent(PolicyEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Boundary:  "Lifecycle & GCS Snapshot",
			Action:    "Manual SuspendActor (" + actorName + ")",
			Verdict:   "SUSPENDED",
			Detail:    "Operator suspended Incident Actor to Cloud Storage (0 vCPU)",
		})
		writeBrokerJSON(w, http.StatusOK, map[string]string{"actor": actorName, "state": "SUSPENDED"})

	case "workspace":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		// Never wake a SUSPENDED actor on passive UI inspection unless ?wake=true is explicitly requested.
		running := false
		if s.ate != nil {
			if a, err := s.ate.GetActor(r.Context(), &ateapipb.GetActorRequest{
				Actor: &ateapipb.ObjectRef{Atespace: s.cfg.Atespace, Name: actorName},
			}); err == nil {
				running = a.GetStatus().GetState() == ateapipb.ActorState_ACTOR_STATE_RUNNING
			}
		}
		if running || r.URL.Query().Get("wake") == "true" {
			if ws, err := s.fetchLiveActorWorkspace(r.Context(), actorName); err == nil {
				s.mu.Lock()
				s.workspaceCache[actorName] = ws
				s.mu.Unlock()
				writeBrokerJSON(w, http.StatusOK, ws)
				return
			}
		}
		s.mu.Lock()
		cached := s.workspaceCache[actorName]
		s.mu.Unlock()
		writeBrokerJSON(w, http.StatusOK, cached)

	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req ChatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAPIBody)).Decode(&req); err != nil {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid chat request JSON"})
		return
	}
	reqCtx, _, _ := telemetry.ExtractHTTP(r.Context(), r.Header)
	out, statusCode, err := s.executeChatTurn(reqCtx, req)
	if out.TraceID != "" {
		w.Header().Set("X-Trace-Id", out.TraceID)
	}
	if err != nil {
		var blockErr *ModelArmorBlockError
		if errors.As(err, &blockErr) {
			writeBrokerJSON(w, http.StatusForbidden, map[string]any{
				"error":       blockErr.Error(),
				"code":        "MODEL_ARMOR_BLOCKED",
				"blocked":     true,
				"verdict":     "MODEL_ARMOR_BLOCKED",
				"boundary":    "Model Armor",
				"incident_id": blockErr.IncidentID,
				"stage":       blockErr.Stage,
				"reason":      blockErr.Reason,
				"template_id": blockErr.TemplateID,
				"provenance":  blockErr.Provenance,
				"trace_id":    out.TraceID,
			})
			return
		}
		writeBrokerJSON(w, statusCode, map[string]string{"error": err.Error(), "trace_id": out.TraceID})
		return
	}
	writeBrokerJSON(w, http.StatusOK, out)
}

func (s *Server) executeChatTurn(ctx context.Context, req ChatRequest) (ChatResponse, int, error) {
	turnStart := time.Now()
	incID := strings.ToUpper(strings.TrimSpace(req.IncidentID))
	if incID == "" {
		incID = "INC-402"
	}
	actorName := incidentToActorName(incID)
	if !validActorLabel.MatchString(actorName) {
		return ChatResponse{}, http.StatusBadRequest, fmt.Errorf("invalid incident actor name %q", actorName)
	}
	tmpl := firstNonEmpty(req.Template, s.cfg.DefaultTemplate)
	sessID := firstNonEmpty(req.SessionID, actorName)

	turnCtx, rootSC, finishTurn := s.startRootTurnSpan(ctx, incID, actorName, sessID, tmpl)

	sanitizedPrompt := req.Prompt
	if strings.TrimSpace(sanitizedPrompt) != "" {
		promptOut, err := s.modelArmor.ScreenUserPrompt(turnCtx, "operator prompt "+incID, incID, sanitizedPrompt)
		if err != nil {
			s.metrics.ObserveTurn(tmpl, "blocked", time.Since(turnStart))
			finishTurn("ERROR", err.Error(), map[string]any{"turn.outcome": "blocked"})
			return ChatResponse{TraceID: rootSC.TraceID}, http.StatusForbidden, err
		}
		sanitizedPrompt = promptOut.SanitizedText
	}

	incContext := ""
	if req.ConfirmationConfirmed == nil {
		incContext = s.buildIncidentContextString(turnCtx, incID)
		if strings.TrimSpace(incContext) != "" {
			ctxOut, err := s.modelArmor.ScreenUserPrompt(turnCtx, "incident payload "+incID, incID, incContext)
			if err != nil {
				s.metrics.ObserveTurn(tmpl, "blocked", time.Since(turnStart))
				finishTurn("ERROR", err.Error(), map[string]any{"turn.outcome": "blocked"})
				return ChatResponse{TraceID: rootSC.TraceID}, http.StatusForbidden, err
			}
			incContext = ctxOut.SanitizedText
		}
	}

	if s.ate != nil {
		gwHost := fmt.Sprintf("sre-gateway.%s.svc.cluster.local", s.cfg.Namespace)
		if _, err := ateclient.EnsureActor(turnCtx, s.ate, s.cfg.Atespace, actorName, tmpl, gwHost); err != nil {
			s.metrics.ObserveTurn(tmpl, "error", time.Since(turnStart))
			finishTurn("ERROR", err.Error(), map[string]any{"turn.outcome": "error"})
			return ChatResponse{TraceID: rootSC.TraceID}, http.StatusBadGateway, err
		}
		s.recordPolicyEvent(PolicyEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Boundary:  "Actor Boundary",
			Action:    fmt.Sprintf("EnsureActor (%s/%s)", s.cfg.Atespace, actorName),
			Verdict:   "ALLOW",
			Detail:    fmt.Sprintf("Template %s + default EgressPolicy restricted strictly to %s", tmpl, gwHost),
		})
	}

	turnPayload := agentruntime.TurnRequest{
		IncidentID:            incID,
		SessionID:             sessID,
		UserID:                "sre-operator",
		Prompt:                sanitizedPrompt,
		IncidentContext:       incContext,
		ConfirmationCallID:    req.ConfirmationCallID,
		ConfirmationConfirmed: req.ConfirmationConfirmed,
		GateApprovalToken:     req.gateApprovalToken,
	}

	actorTurn, routerWakeMs, statusCode, err := s.dispatchActorTurn(turnCtx, actorName, turnPayload)
	if err != nil {
		s.metrics.ObserveTurn(tmpl, "error", time.Since(turnStart))
		finishTurn("ERROR", err.Error(), map[string]any{"turn.outcome": "error"})
		return ChatResponse{TraceID: rootSC.TraceID}, statusCode, err
	}

	if strings.TrimSpace(actorTurn.Reply) != "" {
		respOut, err := s.modelArmor.ScreenModelResponse(turnCtx, "model response "+incID, incID, actorTurn.Reply)
		if err != nil {
			s.metrics.ObserveTurn(tmpl, "blocked", time.Since(turnStart))
			finishTurn("ERROR", err.Error(), map[string]any{"turn.outcome": "blocked"})
			return ChatResponse{TraceID: rootSC.TraceID}, http.StatusForbidden, err
		}
		actorTurn.Reply = respOut.SanitizedText
	}

	s.mu.Lock()
	s.workspaceCache[actorName] = actorTurn.Workspace
	s.mu.Unlock()
	s.agentEngine.StampSessionContext(agentruntime.AppName, actorTurn.SessionID, incID, actorTurn.Template)

	actorSuspended := false
	if actorTurn.RemediationGatePending && s.ate != nil {
		suspCtx, _, finishSusp := s.startBrokerSpan(turnCtx, "substrate.suspend_actor", "CLIENT", "Remediation Gate", map[string]any{
			"ate.actor.id":     actorName,
			"ate.atespace":     s.cfg.Atespace,
			"substrate.reason": "remediation_gate",
		})
		if err := ateclient.SuspendActor(suspCtx, s.ate, s.cfg.Atespace, actorName); err != nil {
			finishSusp("ERROR", err.Error(), nil)
			log.Printf("sre-gateway: remediation gate suspend %s/%s failed: %v", s.cfg.Atespace, actorName, err)
		} else {
			finishSusp("OK", "", nil)
			actorSuspended = true
			s.tracker.Forget(actorName)
			s.recordPolicyEvent(PolicyEvent{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Boundary:  "Remediation Gate",
				Action:    "SuspendActor (" + actorName + ")",
				Verdict:   "SUSPENDED",
				Detail:    "ADK toolconfirmation paused turn; checkpointed Incident Actor to 0 vCPU in Cloud Storage awaiting approval",
			})
		}
	}

	s.mu.Lock()
	if actorTurn.RemediationGatePending {
		callID := firstNonEmpty(actorTurn.CallID, actorTurn.ConfirmationCallID)
		s.pendingGates[incID] = RemediationGateDTO{
			IncidentID:     incID,
			ActorName:      actorName,
			SessionID:      actorTurn.SessionID,
			Template:       actorTurn.Template,
			CallID:         callID,
			ToolName:       firstNonEmpty(actorTurn.ToolName, "apply_remediation_pr"),
			Args:           actorTurn.ToolArgs,
			Hint:           actorTurn.ConfirmationHint,
			Branch:         actorTurn.Workspace.Branch,
			CommitSHA:      actorTurn.Workspace.HeadSHA,
			ActorSuspended: actorSuspended,
			CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		}
	} else if req.ConfirmationConfirmed != nil {
		delete(s.pendingGates, incID)
	}
	s.mu.Unlock()
	if actorTurn.RemediationGatePending || req.ConfirmationConfirmed != nil {
		s.savePersistedState()
	}

	resolvedTmpl := firstNonEmpty(actorTurn.Template, tmpl)
	outcome := "ok"
	if actorTurn.RemediationGatePending {
		outcome = "gated"
	}
	s.traces.AssociateTurnMetadata(rootSC.TraceID, incID, actorName, actorTurn.SessionID, resolvedTmpl, nil)
	s.metrics.ObserveTurn(resolvedTmpl, outcome, time.Since(turnStart))
	finishTurn("OK", "", map[string]any{
		"turn.outcome":           outcome,
		"gen_ai.conversation.id": actorTurn.SessionID,
		"ate.actor.template":     resolvedTmpl,
	})

	callID := firstNonEmpty(actorTurn.CallID, actorTurn.ConfirmationCallID)
	steps := actorTurn.ToolSteps
	if steps == nil {
		steps = []agentruntime.ToolStep{}
	}
	return ChatResponse{
		IncidentID:             actorTurn.IncidentID,
		ActorName:              actorName,
		Atespace:               s.cfg.Atespace,
		SessionID:              actorTurn.SessionID,
		Template:               actorTurn.Template,
		Reply:                  actorTurn.Reply,
		Backend:                actorTurn.Backend,
		StarterStub:            actorTurn.StarterStub,
		ToolSteps:              steps,
		Workspace:              actorTurn.Workspace,
		RemediationGatePending: actorTurn.RemediationGatePending,
		ConfirmationCallID:     callID,
		CallID:                 callID,
		ToolName:               actorTurn.ToolName,
		ToolArgs:               actorTurn.ToolArgs,
		ConfirmationHint:       actorTurn.ConfirmationHint,
		TraceID:                rootSC.TraceID,
		ResumeDurationMs:       routerWakeMs,
		DurationMs:             actorTurn.DurationMs,
	}, http.StatusOK, nil
}

func (s *Server) dispatchActorTurn(ctx context.Context, actorName string, payload agentruntime.TurnRequest) (agentruntime.TurnResponse, int64, int, error) {
	done := s.tracker.Begin(actorName)
	defer done()

	routeCtx, wakeSC, finishWake := s.startBrokerSpan(ctx, "atenet.wake_and_route", "CLIENT", "Actor Boundary", map[string]any{
		"ate.actor.id":           actorName,
		"ate.atespace":           s.cfg.Atespace,
		"gen_ai.conversation.id": payload.SessionID,
		"incident.id":            payload.IncidentID,
		"http.request.method":    "POST",
		"url.path":               "/turn",
	})
	s.setActiveActorTrace(actorName, wakeSC)

	raw, err := json.Marshal(payload)
	if err != nil {
		finishWake("ERROR", err.Error(), nil)
		return agentruntime.TurnResponse{}, 0, http.StatusInternalServerError, err
	}
	req, err := http.NewRequestWithContext(routeCtx, http.MethodPost, s.cfg.RouterURL+"/turn", bytes.NewReader(raw))
	if err != nil {
		finishWake("ERROR", err.Error(), nil)
		return agentruntime.TurnResponse{}, 0, http.StatusInternalServerError, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(TargetActorHeader, s.cfg.Atespace+"/"+actorName)
	req.Header.Set(TargetAtespaceHeader, s.cfg.Atespace)
	telemetry.InjectHTTP(routeCtx, req.Header)

	routerStart := time.Now()
	resp, err := s.routerClient.Do(req)
	if err != nil {
		finishWake("ERROR", err.Error(), nil)
		return agentruntime.TurnResponse{}, 0, http.StatusBadGateway, fmt.Errorf("atenet-router: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	routerElapsedMs := time.Since(routerStart).Milliseconds()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := fmt.Sprintf("actor turn returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		finishWake("ERROR", errMsg, map[string]any{"http.response.status_code": resp.StatusCode})
		return agentruntime.TurnResponse{}, 0, resp.StatusCode, errors.New(errMsg)
	}
	var out agentruntime.TurnResponse
	if err := json.Unmarshal(body, &out); err != nil {
		finishWake("ERROR", err.Error(), nil)
		return agentruntime.TurnResponse{}, 0, http.StatusBadGateway, fmt.Errorf("decode actor turn response: %w", err)
	}
	wakeMs := routerElapsedMs - out.DurationMs
	if wakeMs < 1 {
		wakeMs = 1
	}
	finishWake("OK", "", map[string]any{
		"http.response.status_code": resp.StatusCode,
		"ate.actor.template":        out.Template,
		"substrate.wake_ms":         wakeMs,
	})
	return out, wakeMs, http.StatusOK, nil
}

func (s *Server) fetchLiveActorWorkspace(ctx context.Context, actorName string) (agentruntime.WorkspaceState, error) {
	done := s.tracker.Begin(actorName)
	defer done()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.RouterURL+"/workspace/state", nil)
	if err != nil {
		return agentruntime.WorkspaceState{}, err
	}
	req.Header.Set(TargetActorHeader, s.cfg.Atespace+"/"+actorName)
	req.Header.Set(TargetAtespaceHeader, s.cfg.Atespace)
	resp, err := s.routerClient.Do(req)
	if err != nil {
		return agentruntime.WorkspaceState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return agentruntime.WorkspaceState{}, fmt.Errorf("workspace state HTTP %d", resp.StatusCode)
	}
	var ws agentruntime.WorkspaceState
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&ws); err != nil {
		return agentruntime.WorkspaceState{}, err
	}
	return ws, nil
}

// handleOpsProxy exposes read-only incident lookups and Smart HTTP git transport
// on /ops/* so keyless Incident Actors never need direct egress to prod-sim.
func (s *Server) handleOpsProxy(w http.ResponseWriter, r *http.Request) {
	subPath := strings.TrimPrefix(r.URL.Path, "/ops")
	switch {
	case strings.HasPrefix(subPath, "/git/ledger-api"):
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
	case (strings.HasPrefix(subPath, "/api/incidents") || subPath == "/api/commits" || subPath == "/api/pulls") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
	case subPath == "/ledger/status" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		s.proxySimpleGet(w, r, s.cfg.LedgerAPIURL+"/api/status")
		return
	default:
		writeBrokerJSON(w, http.StatusForbidden, map[string]string{"error": "ops broker route denied by policy"})
		return
	}

	target := s.cfg.OpsHubURL + subPath
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	var bodyReader io.Reader
	if r.Body != nil && r.Method == http.MethodPost {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
		if err != nil {
			writeBrokerJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "git payload too large"})
			return
		}
		bodyReader = bytes.NewReader(b)
	}
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, bodyReader)
	if err != nil {
		writeBrokerJSON(w, http.StatusInternalServerError, map[string]string{"error": "invalid upstream ops request"})
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		upReq.Header.Set("Content-Type", ct)
	}
	if ce := r.Header.Get("Content-Encoding"); ce != "" {
		upReq.Header.Set("Content-Encoding", ce)
	}
	resp, err := s.opsClient.Do(upReq)
	if err != nil {
		writeBrokerJSON(w, http.StatusBadGateway, map[string]string{"error": "ops-hub unreachable: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	if strings.HasPrefix(subPath, "/api/incidents/") && resp.StatusCode == http.StatusOK {
		rawInc, readErr := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
		if readErr != nil {
			writeBrokerJSON(w, http.StatusBadGateway, map[string]string{"error": "read ops-hub incident failed"})
			return
		}
		incID := strings.ToUpper(strings.TrimPrefix(subPath, "/api/incidents/"))
		if _, blockErr := s.modelArmor.ScreenUserPrompt(r.Context(), "ops incident lookup "+incID, incID, string(rawInc)); blockErr != nil {
			writeBrokerJSON(w, http.StatusForbidden, map[string]any{
				"error":   blockErr.Error(),
				"blocked": true,
				"verdict": "MODEL_ARMOR_BLOCKED",
			})
			return
		}
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(rawInc)
		return
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 16<<20))
}

func (s *Server) proxySimpleGet(w http.ResponseWriter, r *http.Request, target string) {
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		writeBrokerJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	resp, err := s.opsClient.Do(upReq)
	if err != nil {
		writeBrokerJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

func (s *Server) listActorSummaries(ctx context.Context) ([]ActorSummary, error) {
	if s.ate == nil {
		return []ActorSummary{}, nil
	}
	rawActors, err := ateclient.ListAllActors(ctx, s.ate, s.cfg.Atespace)
	if err != nil {
		return nil, err
	}
	out := make([]ActorSummary, 0, len(rawActors))
	for _, a := range rawActors {
		name := a.GetMetadata().GetName()
		rawState := strings.TrimPrefix(a.GetStatus().GetState().String(), "ACTOR_STATE_")
		pod := a.GetStatus().GetWorkerAssignment().GetWorkerPod()
		idleSec := s.tracker.ObserveIdleSeconds(name)
		badge := "Suspended · 0 vCPU · <400ms wake"
		if rawState == "RUNNING" {
			if pod != "" {
				badge = "Running on " + pod
			} else {
				badge = "Running"
			}
		}
		out = append(out, ActorSummary{
			Name:         name,
			Atespace:     a.GetMetadata().GetAtespace(),
			Template:     a.GetActorTemplate().GetName(),
			State:        rawState,
			WorkerPod:    pod,
			IdleSeconds:  idleSec,
			ComputeBadge: badge,
		})
	}
	return out, nil
}

func (s *Server) fetchIncidentsFromOpsHub(ctx context.Context) []opshub.Incident {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, s.cfg.OpsHubURL+"/api/incidents", nil)
	if err != nil {
		return nil
	}
	resp, err := s.opsClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var list []opshub.Incident
	_ = json.NewDecoder(io.LimitReader(resp.Body, 512<<10)).Decode(&list)
	return list
}

func (s *Server) fetchPullsFromOpsHub(ctx context.Context) []opshub.PullRequest {
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, s.cfg.OpsHubURL+"/api/pulls", nil)
	if err != nil {
		return []opshub.PullRequest{}
	}
	resp, err := s.opsClient.Do(req)
	if err != nil {
		return []opshub.PullRequest{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return []opshub.PullRequest{}
	}
	var pulls []opshub.PullRequest
	_ = json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&pulls)
	if pulls == nil {
		return []opshub.PullRequest{}
	}
	return pulls
}

func (s *Server) buildIncidentContextString(ctx context.Context, incID string) string {
	for _, inc := range s.fetchIncidentsFromOpsHub(ctx) {
		if strings.EqualFold(inc.ID, incID) {
			var b strings.Builder
			b.WriteString("Title: " + inc.Title + "\n")
			b.WriteString("Severity: " + inc.Severity + " (" + inc.Status + ")\n")
			b.WriteString("Summary: " + inc.Summary + "\n")
			if len(inc.Symptoms) > 0 {
				b.WriteString("Symptoms:\n- " + strings.Join(inc.Symptoms, "\n- ") + "\n")
			}
			if inc.CustomerNote != "" {
				b.WriteString("Attached Note:\n" + inc.CustomerNote + "\n")
			}
			return b.String()
		}
	}
	return ""
}

func (s *Server) fetchLedgerStatus(ctx context.Context) (*ledgersvc.StatusSnapshot, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, s.cfg.LedgerAPIURL+"/api/status", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.opsClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ledger status HTTP %d", resp.StatusCode)
	}
	var snap ledgersvc.StatusSnapshot
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func incidentToActorName(incID string) string {
	cleaned := strings.ToLower(strings.TrimSpace(incID))
	cleaned = strings.ReplaceAll(cleaned, "_", "-")
	return cleaned
}

func setConsoleSecurityHeaders(w http.ResponseWriter, noStore bool) {
	h := w.Header()
	h.Set("Content-Security-Policy", consoleCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if noStore {
		h.Set("Cache-Control", "no-store")
	}
}

func verifyOriginAndCSRF(r *http.Request) error {
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
			return errors.New("cross-origin state-changing request denied")
		}
	}
	if fetchSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); fetchSite != "" &&
		fetchSite != "same-origin" && fetchSite != "none" {
		return errors.New("cross-site state-changing request denied")
	}
	if hasBrowserContext(r) {
		cookieTok := readCSRFCookieValue(r)
		hdrTok := strings.TrimSpace(r.Header.Get(csrfHeaderName))
		if len(cookieTok) < 32 || len(hdrTok) != len(cookieTok) || subtle.ConstantTimeCompare([]byte(hdrTok), []byte(cookieTok)) != 1 {
			return errors.New("missing or invalid CSRF cookie or X-CSRF-Token")
		}
		return nil
	}
	// TODO(security): Production deployments behind Identity-Aware Proxy (IAP) enforce authenticated OIDC/IAP JWTs on all mutating /api/* endpoints.
	return nil
}

func hasBrowserContext(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Cookie")) != "" || strings.TrimSpace(r.Header.Get("Origin")) != "" {
		return true
	}
	for k, vals := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "sec-fetch-") {
			for _, v := range vals {
				if strings.TrimSpace(v) != "" {
					return true
				}
			}
		}
	}
	return false
}

func readCSRFCookieValue(r *http.Request) string {
	if isHTTPSRequest(r) {
		return readCookieValue(r, csrfHostCookieName)
	}
	if v := readCookieValue(r, csrfCookieName); v != "" {
		return v
	}
	return readCookieValue(r, csrfHostCookieName)
}

// hasValidAdminToken reports whether a non-browser CLI mutation satisfies the
// configured X-Broker-Admin-Token guard. Browser sessions already verified by
// verifyOriginAndCSRF (carrying the CSRF cookie and matching X-CSRF-Token) or
// brokers configured without AdminToken return true.
func (s *Server) hasValidAdminToken(r *http.Request) bool {
	want := strings.TrimSpace(s.cfg.AdminToken)
	if want == "" || readCSRFCookieValue(r) != "" {
		return true
	}
	got := strings.TrimSpace(r.Header.Get("X-Broker-Admin-Token"))
	return len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func readCookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(c.Value)
}

func isHTTPSRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func randomHex(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func writeBrokerJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
