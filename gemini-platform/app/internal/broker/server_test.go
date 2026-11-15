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

package broker_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/agentruntime"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/ateclient"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/ateclient/atetest"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/broker"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/brokerclient"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/ledgersvc"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/opshub"
	ateapipb "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
)

func TestEndToEndGovernedBrokerTurnAndIdleSuspend(t *testing.T) {
	// 1. Start degraded Target Service (ledger-api) + Operations Hub (ops-hub) on 127.0.0.1.
	ledger := ledgersvc.NewServer(ledgersvc.Options{
		Config:    ledgersvc.RuntimeConfig{MaxOpenConns: 2, IdleTimeoutSeconds: 0, AcquireTimeoutMs: 60},
		LogOutput: io.Discard,
	})
	t.Cleanup(ledger.Close)
	_ = ledger.ExecuteTransfer(context.Background(), "bad-1")
	_ = ledger.ExecuteTransfer(context.Background(), "bad-2")
	ledgerTS := httptest.NewServer(ledger.Handler())
	t.Cleanup(ledgerTS.Close)

	ops, err := opshub.NewServer(opshub.Options{
		DataDir:      t.TempDir(),
		SimNamespace: "prod-sim",
		LedgerAPIURL: ledgerTS.URL,
	})
	if err != nil {
		t.Fatalf("opshub.NewServer: %v", err)
	}
	opsTS := httptest.NewServer(ops.Handler())
	t.Cleanup(opsTS.Close)

	// 2. Start fake ateapi gRPC control plane over TLS on 127.0.0.1.
	fakeATE := atetest.NewFakeControl()
	ateAddr, caFile, tokenFile := atetest.StartServer(t, fakeATE)

	// 3. Reserve a 127.0.0.1 listener for sre-gateway so the keyless Incident Actor knows BROKER_URL.
	gwLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen broker: %v", err)
	}
	brokerURL := "http://" + gwLis.Addr().String()

	// 4. Start keyless ADK Go v2 Incident Actor (sre-agent-v1) wired to brokerURL.
	wsDir := t.TempDir()
	actorSrv, err := agentruntime.NewServer(agentruntime.Config{
		WorkspaceDir: wsDir,
		BrokerURL:    brokerURL,
		ModelName:    "gemini-3.8-flash",
		TemplateName: "sre-agent-v1",
		BuildRunner: func(ctx context.Context, cfg agentruntime.Config, wsTools []tool.Tool) (*runner.Runner, error) {
			llm, err := brokerclient.NewModel(ctx, cfg.BrokerURL, cfg.ModelName, nil)
			if err != nil {
				return nil, err
			}
			a, err := llmagent.New(llmagent.Config{
				Name:  "sre_triage_agent",
				Model: llm,
				InstructionProvider: func(agent.ReadonlyContext) (string, error) {
					return agentruntime.DefaultInstruction, nil
				},
				Tools: wsTools,
			})
			if err != nil {
				return nil, err
			}
			return runner.New(runner.Config{
				AppName:           agentruntime.AppName,
				Agent:             a,
				SessionService:    session.InMemoryService(),
				AutoCreateSession: true,
			})
		},
	})
	if err != nil {
		t.Fatalf("agentruntime.NewServer: %v", err)
	}
	actorTS := httptest.NewServer(actorSrv.Handler())
	t.Cleanup(actorTS.Close)

	// 5. Start fake atenet-router seam verifying ate-target-actor and ate-target-atespace headers.
	var routerCalls atomic.Int64
	var lastTargetActor, lastTargetAtespace atomic.Value
	routerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routerCalls.Add(1)
		lastTargetActor.Store(r.Header.Get("ate-target-actor"))
		lastTargetAtespace.Store(r.Header.Get("ate-target-atespace"))
		// Simulate router waking the actor in ateapi on ingress traffic.
		fakeATE.MarkRunning("workshop-301", "inc-402", "sre-workers-0")

		fwdReq, err := http.NewRequestWithContext(r.Context(), r.Method, actorTS.URL+r.URL.RequestURI(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fwdReq.Header = r.Header.Clone()
		resp, err := actorTS.Client().Do(fwdReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(routerTS.Close)

	// 6. Start Governed Broker (sre-gateway) on gwLis with MOCK_LLM=true and short IdleAfter.
	gwSrv, err := broker.NewServer(broker.Config{
		Namespace:       "workshop-301",
		Atespace:        "workshop-301",
		SimNamespace:    "prod-sim",
		ProjectID:       "demo-project",
		ModelLocation:   "global",
		GeminiModel:     "gemini-3.8-flash",
		MockLLM:         true,
		DefaultTemplate: "sre-agent-v1",
		SelfURL:         brokerURL,
		RouterURL:       routerTS.URL,
		OpsHubURL:       opsTS.URL,
		LedgerAPIURL:    ledgerTS.URL,
		IdleAfter:       40 * time.Millisecond,
		SPIFFEMode:      "wif",
		SPIFFEDomain:    "demo-project.svc.id.goog",
		ATE: ateclient.Options{
			Addr:      ateAddr,
			CAFile:    caFile,
			TokenFile: tokenFile,
		},
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = gwSrv.Close() })

	httpSrv := &http.Server{Handler: gwSrv.Handler()}
	go func() { _ = httpSrv.Serve(gwLis) }()
	t.Cleanup(func() { _ = httpSrv.Close() })

	// 7. Dispatch Turn 1 via POST /api/chat for INC-402.
	chatReq, _ := json.Marshal(broker.ChatRequest{
		IncidentID: "INC-402",
		Prompt:     "Investigate INC-402 in prod-sim and identify the root cause.",
	})
	chatResp, err := http.Post(brokerURL+"/api/chat", "application/json", bytes.NewReader(chatReq))
	if err != nil {
		t.Fatalf("POST /api/chat: %v", err)
	}
	defer chatResp.Body.Close()
	if chatResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(chatResp.Body)
		t.Fatalf("POST /api/chat status = %d, body = %s", chatResp.StatusCode, string(raw))
	}

	var turnOut broker.ChatResponse
	if err := json.NewDecoder(chatResp.Body).Decode(&turnOut); err != nil {
		t.Fatalf("decode ChatResponse: %v", err)
	}
	if turnOut.ActorName != "inc-402" {
		t.Errorf("ActorName = %q, want inc-402", turnOut.ActorName)
	}
	if !strings.Contains(turnOut.Reply, "max_open_conns") || !strings.Contains(turnOut.Reply, "ReconcileTransfer") {
		t.Errorf("Reply = %q, want root cause citing max_open_conns and ReconcileTransfer", turnOut.Reply)
	}
	if len(turnOut.ToolSteps) < 3 {
		t.Errorf("ToolSteps len = %d, want >= 3 (clone_repo -> read_workspace_file -> run_workspace_command)", len(turnOut.ToolSteps))
	}
	if !turnOut.Workspace.Cloned {
		t.Errorf("Workspace.Cloned = false, want true")
	}

	// Verify router headers and EnsureActor + EgressPolicy in ateapi.
	if got := lastTargetActor.Load(); got != "workshop-301/inc-402" {
		t.Errorf("ate-target-actor = %v, want workshop-301/inc-402", got)
	}
	if got := lastTargetAtespace.Load(); got != "workshop-301" {
		t.Errorf("ate-target-atespace = %v, want workshop-301", got)
	}
	pol := fakeATE.Policy("workshop-301", "inc-402")
	if pol == nil || len(pol.GetRules()) == 0 {
		t.Fatalf("expected default EgressPolicy on workshop-301/inc-402, got %v", pol)
	}
	hosts := pol.GetRules()[0].GetHttp().GetHostnames()
	if len(hosts) != 1 || hosts[0] != "sre-gateway.workshop-301.svc.cluster.local" {
		t.Errorf("EgressPolicy hostnames = %v, want [sre-gateway.workshop-301.svc.cluster.local]", hosts)
	}

	// 8. Verify idle-suspender transitions inc-402 to SUSPENDED (0 vCPU) after IdleAfter,
	// and reading cached workspace state does NOT wake the actor.
	time.Sleep(60 * time.Millisecond)
	if err := gwSrv.RunIdleTick(context.Background()); err != nil {
		t.Fatalf("RunIdleTick: %v", err)
	}
	actorObj := fakeATE.Actor("workshop-301", "inc-402")
	if actorObj == nil || actorObj.GetStatus().GetState() != ateapipb.ActorState_ACTOR_STATE_SUSPENDED {
		t.Fatalf("actor state after idle tick = %v, want ACTOR_STATE_SUSPENDED", actorObj)
	}

	callsBefore := routerCalls.Load()
	actorsResp, err := http.Get(brokerURL + "/api/actors")
	if err != nil {
		t.Fatalf("GET /api/actors: %v", err)
	}
	defer actorsResp.Body.Close()
	var actorsList []broker.ActorSummary
	if err := json.NewDecoder(actorsResp.Body).Decode(&actorsList); err != nil {
		t.Fatalf("decode actors: %v", err)
	}
	if len(actorsList) != 1 || actorsList[0].State != "SUSPENDED" || !strings.Contains(actorsList[0].ComputeBadge, "0 vCPU") {
		t.Errorf("actors list = %+v, want SUSPENDED with 0 vCPU badge", actorsList)
	}

	wsCacheResp, err := http.Get(brokerURL + "/api/actors/inc-402/workspace")
	if err != nil {
		t.Fatalf("GET /api/actors/inc-402/workspace: %v", err)
	}
	wsCacheResp.Body.Close()
	if routerCalls.Load() != callsBefore {
		t.Errorf("zero-wake workspace endpoint triggered router traffic while actor was SUSPENDED")
	}
}

func TestRouterMTLSAndConsoleSecurityContracts(t *testing.T) {
	// Verify mTLS client cert presentation to atenet-router.
	serverTLS, clientCertFile, caFile := atetest.MutualTLSFiles(t, "atenet-router.ate-system.svc")
	mtlsRouter := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "missing client cert", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agentruntime.TurnResponse{
			IncidentID: "INC-402",
			SessionID:  "inc-402",
			Template:   "sre-agent-v1",
			Reply:      "mTLS verified turn response",
			Backend:    "adk-gemini",
		})
	}))
	mtlsRouter.TLS = serverTLS
	mtlsRouter.StartTLS()
	t.Cleanup(mtlsRouter.Close)

	fakeATE := atetest.NewFakeControl()
	ateAddr, ateCA, ateTok := atetest.StartServer(t, fakeATE)

	ops, err := opshub.NewServer(opshub.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	opsTS := httptest.NewServer(ops.Handler())
	t.Cleanup(opsTS.Close)

	gwSrv, err := broker.NewServer(broker.Config{
		Namespace:        "workshop-301",
		Atespace:         "workshop-301",
		SimNamespace:     "prod-sim",
		ProjectID:        "demo-project",
		MockLLM:          true,
		DefaultTemplate:  "sre-agent-v1",
		RouterURL:        mtlsRouter.URL,
		RouterServerName: "atenet-router.ate-system.svc",
		RouterCAFile:     caFile,
		RouterClientCert: clientCertFile,
		OpsHubURL:        opsTS.URL,
		IdleAfter:        15 * time.Second,
		ATE: ateclient.Options{
			Addr:      ateAddr,
			CAFile:    ateCA,
			TokenFile: ateTok,
		},
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = gwSrv.Close() })

	gwTS := httptest.NewServer(gwSrv.Handler())
	t.Cleanup(gwTS.Close)

	// Check all 4 Cloud Console path-based routes and security headers.
	for _, route := range []string{"/", "/sessions", "/catalog", "/about"} {
		resp, err := http.Get(gwTS.URL + route)
		if err != nil {
			t.Fatalf("GET %s: %v", route, err)
		}
		htmlBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", route, resp.StatusCode)
		}
		if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("GET %s X-Frame-Options = %q, want DENY", route, got)
		}
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s X-Content-Type-Options = %q, want nosniff", route, got)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("GET %s CSP = %q, missing strict directives", route, csp)
		}
		if !strings.Contains(string(htmlBytes), `data-theme="light"`) {
			t.Errorf("GET %s HTML missing default data-theme=\"light\"", route)
		}
	}

	// Verify Cloud Design System CSS tokens from DESIGN.md.
	cssResp, err := http.Get(gwTS.URL + "/assets/styles.css")
	if err != nil {
		t.Fatalf("GET /assets/styles.css: %v", err)
	}
	cssBytes, _ := io.ReadAll(cssResp.Body)
	cssResp.Body.Close()
	cssText := string(cssBytes)
	for _, token := range []string{"--cm-sys-color-surface", "#ffffff", "#f1f3f4", "#1a73e8", "#202124", "#2a2b2e", "#8ab4f8", "Roboto"} {
		if !strings.Contains(cssText, token) {
			t.Errorf("styles.css missing DESIGN.md token %q", token)
		}
	}
	if strings.Contains(cssText, "backdrop-filter") || strings.Contains(cssText, "uppercase") {
		t.Errorf("styles.css contains forbidden glassmorphism or uppercase styling")
	}

	// Verify cross-origin POST rejection and CSRF enforcement when cookie is present.
	crossReq, _ := http.NewRequest(http.MethodPost, gwTS.URL+"/api/chat", strings.NewReader(`{"incident_id":"INC-402"}`))
	crossReq.Header.Set("Content-Type", "application/json")
	crossReq.Header.Set("Origin", "https://attacker.example")
	crossResp, err := http.DefaultClient.Do(crossReq)
	if err != nil {
		t.Fatal(err)
	}
	crossResp.Body.Close()
	if crossResp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin POST status = %d, want 403", crossResp.StatusCode)
	}

	// Fetch session bootstrap to obtain HttpOnly CSRF cookie + token, then test missing vs valid X-CSRF-Token.
	sessResp, err := http.Get(gwTS.URL + "/api/session")
	if err != nil {
		t.Fatalf("GET /api/session: %v", err)
	}
	var sessInfo struct {
		CSRFToken string `json:"csrf_token"`
	}
	_ = json.NewDecoder(sessResp.Body).Decode(&sessInfo)
	cookies := sessResp.Cookies()
	sessResp.Body.Close()
	if sessInfo.CSRFToken == "" || len(cookies) == 0 {
		t.Fatalf("expected CSRF token and cookie from /api/session")
	}

	noHeaderReq, _ := http.NewRequest(http.MethodPost, gwTS.URL+"/api/chat", strings.NewReader(`{"incident_id":"INC-402"}`))
	noHeaderReq.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		noHeaderReq.AddCookie(c)
	}
	noHeaderResp, err := http.DefaultClient.Do(noHeaderReq)
	if err != nil {
		t.Fatal(err)
	}
	noHeaderResp.Body.Close()
	if noHeaderResp.StatusCode != http.StatusForbidden {
		t.Errorf("cookie-bearing POST without X-CSRF-Token status = %d, want 403", noHeaderResp.StatusCode)
	}

	validReq, _ := http.NewRequest(http.MethodPost, gwTS.URL+"/api/chat", strings.NewReader(`{"incident_id":"INC-402","prompt":"hello"}`))
	validReq.Header.Set("Content-Type", "application/json")
	validReq.Header.Set("X-CSRF-Token", sessInfo.CSRFToken)
	for _, c := range cookies {
		validReq.AddCookie(c)
	}
	validResp, err := http.DefaultClient.Do(validReq)
	if err != nil {
		t.Fatal(err)
	}
	defer validResp.Body.Close()
	if validResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(validResp.Body)
		t.Fatalf("valid mTLS + CSRF POST /api/chat status = %d, body = %s", validResp.StatusCode, string(raw))
	}

	// Avoid unused imports when building helper signatures.
	_ = tls.VersionTLS12
	_ = x509.KeyUsageDigitalSignature
	_ = os.DevNull
	_ = filepath.Separator
}

func TestHTTPSHostCSRFCookieBrowserContextAndDesignCompliance(t *testing.T) {
	fakeRouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agentruntime.TurnResponse{
			IncidentID: "INC-402",
			SessionID:  "inc-402",
			Template:   "sre-agent-v1",
			Reply:      "ok",
			Backend:    "adk-gemini",
		})
	}))
	t.Cleanup(fakeRouter.Close)

	gwSrv, err := broker.NewServer(broker.Config{
		Namespace:       "workshop-301",
		Atespace:        "workshop-301",
		SimNamespace:    "prod-sim",
		ProjectID:       "demo-project",
		MockLLM:         true,
		DefaultTemplate: "sre-agent-v1",
		RouterURL:       fakeRouter.URL,
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = gwSrv.Close() })
	gwTS := httptest.NewServer(gwSrv.Handler())
	t.Cleanup(gwTS.Close)

	// 1. HTTPS session bootstrap (X-Forwarded-Proto: https) must issue __Host-sre_csrf with Secure, HttpOnly, SameSite=Lax, Path=/.
	httpsSessReq, _ := http.NewRequest(http.MethodGet, gwTS.URL+"/api/session", nil)
	httpsSessReq.Header.Set("X-Forwarded-Proto", "https")
	httpsSessResp, err := http.DefaultClient.Do(httpsSessReq)
	if err != nil {
		t.Fatalf("GET /api/session (https): %v", err)
	}
	var sessDoc struct {
		CSRFToken string `json:"csrf_token"`
	}
	_ = json.NewDecoder(httpsSessResp.Body).Decode(&sessDoc)
	httpsCookies := httpsSessResp.Cookies()
	httpsSessResp.Body.Close()

	var hostCookie *http.Cookie
	for _, c := range httpsCookies {
		if c.Name == "__Host-sre_csrf" {
			hostCookie = c
		}
	}
	if hostCookie == nil {
		t.Fatalf("expected __Host-sre_csrf cookie over HTTPS, got %+v", httpsCookies)
	}
	if !hostCookie.Secure || !hostCookie.HttpOnly || hostCookie.SameSite != http.SameSiteLaxMode || hostCookie.Path != "/" {
		t.Errorf("__Host-sre_csrf flags = %+v, want Secure=true HttpOnly=true SameSite=Lax Path=/", hostCookie)
	}

	// 2. Any state-changing /api/* request carrying browser context (Cookie, Origin, or Sec-Fetch-*)
	// without both CSRF cookie and matching X-CSRF-Token must be rejected with HTTP 403.
	browserCases := []struct {
		name string
		hdr  map[string]string
	}{
		{name: "unrelated cookie without CSRF", hdr: map[string]string{"Cookie": "sre_theme=dark"}},
		{name: "same-origin Origin without CSRF", hdr: map[string]string{"Origin": gwTS.URL}},
		{name: "Sec-Fetch-Site same-origin without CSRF", hdr: map[string]string{"Sec-Fetch-Site": "same-origin"}},
		{name: "Sec-Fetch-Mode cors without CSRF", hdr: map[string]string{"Sec-Fetch-Mode": "cors"}},
	}
	for _, tc := range browserCases {
		req, _ := http.NewRequest(http.MethodPost, gwTS.URL+"/api/chat", strings.NewReader(`{"incident_id":"INC-402"}`))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range tc.hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403 Forbidden", tc.name, resp.StatusCode)
		}
	}

	// 3. HTTPS POST /api/chat with valid __Host-sre_csrf cookie + X-CSRF-Token succeeds.
	okHTTPSReq, _ := http.NewRequest(http.MethodPost, gwTS.URL+"/api/chat", strings.NewReader(`{"incident_id":"INC-402","prompt":"hello"}`))
	okHTTPSReq.Header.Set("Content-Type", "application/json")
	okHTTPSReq.Header.Set("X-Forwarded-Proto", "https")
	okHTTPSReq.Header.Set("Sec-Fetch-Site", "same-origin")
	okHTTPSReq.Header.Set("X-CSRF-Token", sessDoc.CSRFToken)
	okHTTPSReq.AddCookie(hostCookie)
	okHTTPSResp, err := http.DefaultClient.Do(okHTTPSReq)
	if err != nil {
		t.Fatalf("HTTPS POST /api/chat: %v", err)
	}
	okHTTPSResp.Body.Close()
	if okHTTPSResp.StatusCode != http.StatusOK {
		t.Fatalf("HTTPS POST /api/chat status = %d, want 200 OK", okHTTPSResp.StatusCode)
	}

	// 4. Verify DESIGN.md strict sentence-case labels, icon-only #btn-cmdk, and waterfall radius token.
	rootResp, err := http.Get(gwTS.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	rootBytes, _ := io.ReadAll(rootResp.Body)
	rootResp.Body.Close()
	htmlText := string(rootBytes)
	for _, wantHTML := range []string{
		`id="btn-cmdk" class="icon-btn"`,
		`aria-label="Operator &amp; scenario controls (⌘K)"`,
		`Target service telemetry (ledger-api)`,
		`Agent Registry · governed MCP servers`,
		`Governed workspace tools, procedural skills &amp; MCP servers`,
	} {
		if !strings.Contains(htmlText, wantHTML) {
			t.Errorf("index.html missing DESIGN.md snippet %q", wantHTML)
		}
	}
	if strings.Contains(htmlText, "cmdk-kbd") {
		t.Errorf("#btn-cmdk must be icon-only in the 48px Platform Bar (found visible cmdk-kbd label)")
	}

	jsResp, err := http.Get(gwTS.URL + "/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	jsBytes, _ := io.ReadAll(jsResp.Body)
	jsResp.Body.Close()
	if !strings.Contains(string(jsBytes), `"Governed broker session ledger"`) {
		t.Errorf("app.js missing sentence-case \"Governed broker session ledger\"")
	}

	cssResp, err := http.Get(gwTS.URL + "/assets/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	cssBytes, _ := io.ReadAll(cssResp.Body)
	cssResp.Body.Close()
	if strings.Contains(string(cssBytes), "border-radius: 2px") {
		t.Errorf("styles.css contains non-token border-radius: 2px; expected var(--radius-xs)")
	}
}

func TestChatZeroToolTurnAndEmptyPullsSerializeEmptyArrays(t *testing.T) {
	ops, err := opshub.NewServer(opshub.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opshub.NewServer: %v", err)
	}
	opsTS := httptest.NewServer(ops.Handler())
	t.Cleanup(opsTS.Close)

	fakeRouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/turn" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"incident_id":"INC-403","session_id":"inc-403","template":"sre-agent-v2","reply":"Recalled INC-402 pool sizing from preload_memory.","backend":"adk-gemini","workspace":{"cloned":false},"remediation_gate_pending":false,"duration_ms":12}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(fakeRouter.Close)

	gwSrv, err := broker.NewServer(broker.Config{
		Namespace:       "workshop-301",
		Atespace:        "workshop-301",
		SimNamespace:    "prod-sim",
		ProjectID:       "demo-project",
		MockLLM:         true,
		DefaultTemplate: "sre-agent-v2",
		RouterURL:       fakeRouter.URL,
		OpsHubURL:       opsTS.URL,
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = gwSrv.Close() })
	gwTS := httptest.NewServer(gwSrv.Handler())
	t.Cleanup(gwTS.Close)

	chatResp, err := gwTS.Client().Post(gwTS.URL+"/api/chat", "application/json", strings.NewReader(`{"incident_id":"INC-403","template":"sre-agent-v2","prompt":"Triage INC-403"}`))
	if err != nil {
		t.Fatalf("POST /api/chat: %v", err)
	}
	chatRaw, _ := io.ReadAll(chatResp.Body)
	_ = chatResp.Body.Close()
	if !strings.Contains(string(chatRaw), `"tool_steps":[]`) {
		t.Fatalf("POST /api/chat raw JSON missing \"tool_steps\":[]: %s", string(chatRaw))
	}

	pullsResp, err := gwTS.Client().Get(gwTS.URL + "/ops/api/pulls")
	if err != nil {
		t.Fatalf("GET /ops/api/pulls: %v", err)
	}
	pullsRaw, _ := io.ReadAll(pullsResp.Body)
	_ = pullsResp.Body.Close()
	if strings.TrimSpace(string(pullsRaw)) != "[]" {
		t.Fatalf("GET /ops/api/pulls raw JSON = %q, want \"[]\"", strings.TrimSpace(string(pullsRaw)))
	}
}
