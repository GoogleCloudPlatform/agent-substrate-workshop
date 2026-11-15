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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/agentruntime"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ateclient"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ateclient/atetest"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/broker"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ledgersvc"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/opshub"
	ateapipb "go.ate.dev/substrate/api/ateapi/v1"
)

func writeSPIFFEBundlePEM(t *testing.T, dir, filename, spiffeURI string, notAfter time.Time) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	parsedURI, err := url.Parse(spiffeURI)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", spiffeURI, err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(424242),
		Subject: pkix.Name{
			CommonName: "sre-gateway",
		},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		URIs:                  []*url.URL{parsedURI},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}

	fullPath := filepath.Join(dir, filename)
	f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encode cert PEM: %v", err)
	}
	if err := pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		t.Fatalf("encode key PEM: %v", err)
	}
	return fullPath
}

func TestModule05PodAgentIdentitySVIDAndAtenetInspection(t *testing.T) {
	dir := t.TempDir()
	wantAgentSPIFFE := "spiffe://agents.global.org-998877.system.id.goog/resources/container/projects/112233/locations/us-central1-a/clusters/ate-cluster/ns/workshop-301/sa/sre-gateway"
	wantAtenetSPIFFE := "spiffe://cluster.local/ns/workshop-301/sa/sre-gateway"
	wantExpiry := time.Now().UTC().Add(20 * time.Hour).Truncate(time.Second)

	agentSVIDFile := writeSPIFFEBundlePEM(t, dir, "x509.credential-bundle.private-key.pem", wantAgentSPIFFE, wantExpiry)
	atenetCertFile := writeSPIFFEBundlePEM(t, dir, "credential-bundle.pem", wantAtenetSPIFFE, wantExpiry)

	srv, err := broker.NewServer(broker.Config{
		Namespace:             "workshop-301",
		Atespace:              "workshop-301",
		SimNamespace:          "prod-sim",
		ProjectID:             "demo-project",
		Region:                "us-central1",
		MockLLM:               true,
		SPIFFEMode:            "wif",
		SPIFFEDomain:          "demo-project.svc.id.goog",
		AgentIdentityCertFile: agentSVIDFile,
		RouterClientCert:      atenetCertFile,
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/governance")
	if err != nil {
		t.Fatalf("GET /api/governance: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/governance status = %d, want 200", resp.StatusCode)
	}

	var gov struct {
		SPIFFEMode   string                `json:"spiffe_mode"`
		SPIFFEDomain string                `json:"spiffe_domain"`
		Identity     broker.IdentityStatus `json:"identity"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&gov); err != nil {
		t.Fatalf("decode /api/governance: %v", err)
	}

	if gov.Identity.Mode != "org" {
		t.Errorf("Identity.Mode = %q, want org (auto-detected from mounted X.509 SVID)", gov.Identity.Mode)
	}
	if gov.Identity.TrustDomain != "agents.global.org-998877.system.id.goog" {
		t.Errorf("Identity.TrustDomain = %q, want agents.global.org-998877.system.id.goog", gov.Identity.TrustDomain)
	}
	if gov.Identity.Principal != wantAgentSPIFFE {
		t.Errorf("Identity.Principal = %q, want %q", gov.Identity.Principal, wantAgentSPIFFE)
	}
	if !gov.Identity.SVIDMounted || !gov.Identity.SVIDValid {
		t.Errorf("Identity SVID flags = (mounted=%t, valid=%t), want (true, true)", gov.Identity.SVIDMounted, gov.Identity.SVIDValid)
	}
	if gov.Identity.SVIDExpiresAt != wantExpiry.Format(time.RFC3339) {
		t.Errorf("Identity.SVIDExpiresAt = %q, want %q", gov.Identity.SVIDExpiresAt, wantExpiry.Format(time.RFC3339))
	}
	if !gov.Identity.AtenetCertMounted || gov.Identity.AtenetSPIFFEID != wantAtenetSPIFFE {
		t.Errorf("Identity atenet = (mounted=%t, id=%q), want (true, %q)", gov.Identity.AtenetCertMounted, gov.Identity.AtenetSPIFFEID, wantAtenetSPIFFE)
	}

	// Verify WIF fallback when no Pod Agent Identity X.509 SVID file is mounted.
	wifSrv, err := broker.NewServer(broker.Config{
		Namespace:             "workshop-301",
		Atespace:              "workshop-301",
		SimNamespace:          "prod-sim",
		ProjectID:             "demo-project",
		Region:                "us-central1",
		MockLLM:               true,
		SPIFFEMode:            "wif",
		SPIFFEDomain:          "demo-project.svc.id.goog",
		AgentIdentityCertFile: filepath.Join(dir, "missing-svid.pem"),
		RouterClientCert:      filepath.Join(dir, "missing-atenet.pem"),
	})
	if err != nil {
		t.Fatalf("broker.NewServer(wif): %v", err)
	}
	t.Cleanup(func() { _ = wifSrv.Close() })
	wifTS := httptest.NewServer(wifSrv.Handler())
	t.Cleanup(wifTS.Close)

	wifResp, err := http.Get(wifTS.URL + "/api/governance")
	if err != nil {
		t.Fatalf("GET /api/governance (wif): %v", err)
	}
	defer wifResp.Body.Close()
	var wifGov struct {
		Identity broker.IdentityStatus `json:"identity"`
	}
	_ = json.NewDecoder(wifResp.Body).Decode(&wifGov)
	if wifGov.Identity.Mode != "wif" || wifGov.Identity.SVIDMounted {
		t.Errorf("WIF Identity = %+v, want Mode=wif SVIDMounted=false", wifGov.Identity)
	}
	wantWIFPrincipal := "spiffe://demo-project.svc.id.goog/ns/workshop-301/sa/sre-gateway"
	if wifGov.Identity.Principal != wantWIFPrincipal {
		t.Errorf("WIF Identity.Principal = %q, want %q", wifGov.Identity.Principal, wantWIFPrincipal)
	}
}

func TestModule05ModelArmorLiveBlockINC409AndSDPRedaction(t *testing.T) {
	var userPromptCalls atomic.Int64
	var modelResponseCalls atomic.Int64

	modelArmorTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		raw, _ := io.ReadAll(r.Body)
		bodyStr := strings.ToLower(string(raw))
		switch r.URL.Path {
		case "/v1/projects/demo-project/locations/us-central1/templates/sre-triage-guard:sanitizeUserPrompt":
			userPromptCalls.Add(1)
			matchState := "NO_MATCH_FOUND"
			if strings.Contains(bodyStr, "ignore all previous instructions") || strings.Contains(bodyStr, "drop_ledger_database") {
				matchState = "MATCH_FOUND"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sanitizationResult": map[string]any{
					"filterMatchState": matchState,
				},
			})
		case "/v1/projects/demo-project/locations/us-central1/templates/sre-triage-guard:sanitizeModelResponse":
			modelResponseCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sanitizationResult": map[string]any{
					"filterMatchState": "NO_MATCH_FOUND",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(modelArmorTS.Close)

	ledger := ledgersvc.NewServer(ledgersvc.Options{
		Config:    ledgersvc.RuntimeConfig{MaxOpenConns: 2, IdleTimeoutSeconds: 0, AcquireTimeoutMs: 60},
		LogOutput: io.Discard,
	})
	t.Cleanup(ledger.Close)
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

	fakeATE := atetest.NewFakeControl()
	ateAddr, caFile, tokenFile := atetest.StartServer(t, fakeATE)

	gwLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen broker: %v", err)
	}
	brokerURL := "http://" + gwLis.Addr().String()

	var actorTurnCount atomic.Int64
	var lastPromptSeen atomic.Value
	actorWorkspaceDir := t.TempDir()
	var actorTS *httptest.Server
	getActor := func() *httptest.Server {
		if actorTS != nil {
			return actorTS
		}
		actorSrv, err := agentruntime.NewServer(agentruntime.Config{
			WorkspaceDir: actorWorkspaceDir,
			BrokerURL:    brokerURL,
			ModelName:    "gemini-3.8-flash",
			TemplateName: "sre-agent-v4",
			BuildRunner:  buildModule04Runner,
		})
		if err != nil {
			t.Fatalf("agentruntime.NewServer: %v", err)
		}
		actorTS = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/turn" {
				actorTurnCount.Add(1)
				raw, _ := io.ReadAll(r.Body)
				lastPromptSeen.Store(string(raw))
				r.Body = io.NopCloser(bytes.NewReader(raw))
			}
			actorSrv.Handler().ServeHTTP(w, r)
		}))
		t.Cleanup(actorTS.Close)
		return actorTS
	}

	routerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := getActor()
		fwdReq, _ := http.NewRequestWithContext(r.Context(), r.Method, target.URL+r.URL.RequestURI(), r.Body)
		fwdReq.Header = r.Header.Clone()
		resp, err := target.Client().Do(fwdReq)
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

	gwSrv, err := broker.NewServer(broker.Config{
		Namespace:             "workshop-301",
		Atespace:              "workshop-301",
		SimNamespace:          "prod-sim",
		ProjectID:             "demo-project",
		Region:                "us-central1",
		ModelLocation:         "global",
		GeminiModel:           "gemini-3.8-flash",
		MockLLM:               true,
		DefaultTemplate:       "sre-agent-v4",
		SelfURL:               brokerURL,
		RouterURL:             routerTS.URL,
		OpsHubURL:             opsTS.URL,
		LedgerAPIURL:          ledgerTS.URL,
		GovernanceEnforced:    true,
		ModelArmorTemplateID:  "sre-triage-guard",
		ModelArmorUpstreamURL: modelArmorTS.URL,
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

	// 1. Investigating INC-409 via POST /api/chat must be hard-blocked (HTTP 403 MODEL_ARMOR_BLOCKED)
	// before waking or invoking the Incident Actor.
	inc409Body, _ := json.Marshal(broker.ChatRequest{
		IncidentID: "INC-409",
		Template:   "sre-agent-v4",
		Prompt:     "Investigate INC-409 and summarize the attached partner webhook note.",
	})
	resp409, err := http.Post(brokerURL+"/api/chat", "application/json", bytes.NewReader(inc409Body))
	if err != nil {
		t.Fatalf("POST /api/chat (INC-409): %v", err)
	}
	raw409, _ := io.ReadAll(resp409.Body)
	resp409.Body.Close()
	if resp409.StatusCode != http.StatusForbidden {
		t.Fatalf("POST /api/chat (INC-409) status = %d (%s), want 403 Forbidden", resp409.StatusCode, string(raw409))
	}
	var blockDoc struct {
		Blocked bool   `json:"blocked"`
		Verdict string `json:"verdict"`
	}
	_ = json.Unmarshal(raw409, &blockDoc)
	if !blockDoc.Blocked || blockDoc.Verdict != "MODEL_ARMOR_BLOCKED" {
		t.Errorf("INC-409 block payload = %s, want blocked=true verdict=MODEL_ARMOR_BLOCKED", string(raw409))
	}
	if actorTurnCount.Load() != 0 {
		t.Fatalf("actorTurnCount after INC-409 block = %d, want 0 (blocked before reaching sandbox)", actorTurnCount.Load())
	}
	if userPromptCalls.Load() == 0 {
		t.Errorf("expected live Model Armor :sanitizeUserPrompt call, got 0")
	}

	// 2. Peer A2A delegation targeting INC-409 via POST /a2a must also return HTTP 403.
	a2a409, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "a2a-409",
		"method":  "message/send",
		"params": map[string]any{
			"contextId": "inc-409",
			"metadata":  map[string]any{"incident_id": "INC-409"},
			"message": map[string]any{
				"role":  "user",
				"parts": []map[string]any{{"kind": "text", "text": "Triage INC-409."}},
			},
		},
	})
	a2aResp, err := http.Post(brokerURL+"/a2a", "application/json", bytes.NewReader(a2a409))
	if err != nil {
		t.Fatalf("POST /a2a (INC-409): %v", err)
	}
	a2aRaw, _ := io.ReadAll(a2aResp.Body)
	a2aResp.Body.Close()
	if a2aResp.StatusCode != http.StatusForbidden || !strings.Contains(string(a2aRaw), "MODEL_ARMOR_BLOCKED") {
		t.Errorf("POST /a2a (INC-409) = %d (%s), want 403 MODEL_ARMOR_BLOCKED", a2aResp.StatusCode, string(a2aRaw))
	}

	// 3. MCP get_incident(INC-409) through /mcp/ops-hub-mcp must also screen the untrusted
	// incident payload and block with HTTP 403 before returning poisoned text to an agent,
	// and drop_ledger_database must be rejected with HTTP 403 by MCP policy.
	mcpGet409, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      11,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "get_incident",
			"arguments": map[string]any{"incident_id": "INC-409"},
		},
	})
	mcp409Resp, err := http.Post(brokerURL+"/mcp/ops-hub-mcp", "application/json", bytes.NewReader(mcpGet409))
	if err != nil {
		t.Fatalf("POST /mcp/ops-hub-mcp get_incident(INC-409): %v", err)
	}
	mcp409Resp.Body.Close()
	if mcp409Resp.StatusCode != http.StatusForbidden {
		t.Errorf("/mcp/ops-hub-mcp get_incident(INC-409) = %d, want 403", mcp409Resp.StatusCode)
	}

	mcpDrop, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      12,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "drop_ledger_database",
			"arguments": map[string]any{"confirm": true},
		},
	})
	dropResp, err := http.Post(brokerURL+"/mcp/ops-hub-mcp", "application/json", bytes.NewReader(mcpDrop))
	if err != nil {
		t.Fatalf("POST /mcp/ops-hub-mcp drop_ledger_database: %v", err)
	}
	dropResp.Body.Close()
	if dropResp.StatusCode != http.StatusForbidden {
		t.Errorf("/mcp/ops-hub-mcp drop_ledger_database = %d, want 403", dropResp.StatusCode)
	}

	// 4. Benign INC-402 prompt containing inline PII (email, SSN, API key) is redacted in-flight
	// without blocking, and :sanitizeModelResponse screens the reply.
	piiPromptBody, _ := json.Marshal(broker.ChatRequest{
		IncidentID: "INC-402",
		Template:   "sre-agent-v4",
		Prompt:     "Investigate INC-402 reported by jane.doe@corp.example.com (SSN 123-45-6789, key AIzaSyDummySecretKeyValue12345) by cloning ledger-api and running ledger-verify.",
	})
	cleanResp, err := http.Post(brokerURL+"/api/chat", "application/json", bytes.NewReader(piiPromptBody))
	if err != nil {
		t.Fatalf("POST /api/chat (INC-402 with PII): %v", err)
	}
	cleanRaw, _ := io.ReadAll(cleanResp.Body)
	cleanResp.Body.Close()
	if cleanResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/chat (INC-402 with PII) = %d (%s), want 200 OK", cleanResp.StatusCode, string(cleanRaw))
	}
	seenTurnPayload, _ := lastPromptSeen.Load().(string)
	for _, rawPII := range []string{"jane.doe@corp.example.com", "123-45-6789", "AIzaSyDummySecretKeyValue12345"} {
		if strings.Contains(seenTurnPayload, rawPII) {
			t.Errorf("raw PII %q leaked across Actor Boundary into TurnRequest: %s", rawPII, seenTurnPayload)
		}
	}
	for _, marker := range []string{"[REDACTED_EMAIL]", "[REDACTED_SSN]", "[REDACTED_API_KEY]"} {
		if !strings.Contains(seenTurnPayload, marker) {
			t.Errorf("TurnRequest missing expected SDP redaction marker %q: %s", marker, seenTurnPayload)
		}
	}
	if modelResponseCalls.Load() == 0 {
		t.Errorf("expected :sanitizeModelResponse call on completed turn, got 0")
	}

	// 5. Verify GET /api/governance records Model Armor blocks, SDP redactions, and MCP 403 denials.
	govResp, err := http.Get(brokerURL + "/api/governance")
	if err != nil {
		t.Fatalf("GET /api/governance: %v", err)
	}
	var govDoc struct {
		ModelArmor broker.ModelArmorStatus `json:"model_armor"`
		MCPPolicy  broker.MCPPolicyStatus  `json:"mcp_policy"`
		Events     []broker.PolicyEvent    `json:"events"`
	}
	_ = json.NewDecoder(govResp.Body).Decode(&govDoc)
	govResp.Body.Close()
	if !govDoc.ModelArmor.Enabled || govDoc.ModelArmor.BlockedCount < 2 || govDoc.ModelArmor.RedactedCount < 1 {
		t.Errorf("ModelArmor status = %+v, want Enabled=true BlockedCount>=2 RedactedCount>=1", govDoc.ModelArmor)
	}
	if !govDoc.MCPPolicy.Enforced || govDoc.MCPPolicy.DeniedCount < 1 {
		t.Errorf("MCPPolicy status = %+v, want Enforced=true DeniedCount>=1", govDoc.MCPPolicy)
	}
}

var testCSRFInputRE = regexp.MustCompile(`name="csrf_token"\s+value="([^"]+)"`)

func completeBrowserPKCEFlow(t *testing.T, authURL string) {
	t.Helper()
	noRedirect := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	getResp, err := noRedirect.Get(authURL)
	if err != nil {
		t.Fatalf("GET auth_url (%s): %v", authURL, err)
	}
	htmlBytes, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET auth_url status = %d (%s), want 200", getResp.StatusCode, string(htmlBytes))
	}

	var csrfCookie *http.Cookie
	for _, c := range getResp.Cookies() {
		if strings.Contains(c.Name, "opshub_csrf") {
			csrfCookie = c
		}
	}
	if csrfCookie == nil {
		t.Fatalf("missing opshub_csrf cookie from %s", authURL)
	}
	m := testCSRFInputRE.FindStringSubmatch(string(htmlBytes))
	if len(m) < 2 {
		t.Fatalf("missing csrf_token hidden input in consent page")
	}

	parsedAuthURL, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authURL: %v", err)
	}
	form := parsedAuthURL.Query()
	form.Set("csrf_token", m[1])
	form.Set("decision", "approve")

	postTarget := parsedAuthURL.Scheme + "://" + parsedAuthURL.Host + "/oauth/authorize"
	postReq, _ := http.NewRequest(http.MethodPost, postTarget, strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(csrfCookie)
	postResp, err := noRedirect.Do(postReq)
	if err != nil {
		t.Fatalf("POST /oauth/authorize: %v", err)
	}
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusFound {
		t.Fatalf("POST /oauth/authorize status = %d, want 302", postResp.StatusCode)
	}
	cbLoc := postResp.Header.Get("Location")
	if strings.Contains(cbLoc, "opshub_at_") || strings.Contains(cbLoc, "access_token") {
		t.Fatalf("callback redirect URL leaked token material: %s", cbLoc)
	}

	cbReq, _ := http.NewRequest(http.MethodGet, cbLoc, nil)
	cbReq.Header.Set("Accept", "application/json")
	cbResp, err := noRedirect.Do(cbReq)
	if err != nil {
		t.Fatalf("GET callback (%s): %v", cbLoc, err)
	}
	cbRaw, _ := io.ReadAll(cbResp.Body)
	cbResp.Body.Close()
	if cbResp.StatusCode != http.StatusOK {
		t.Fatalf("GET callback status = %d (%s), want 200", cbResp.StatusCode, string(cbRaw))
	}
	if strings.Contains(string(cbRaw), "opshub_at_") {
		t.Fatalf("callback JSON leaked raw bearer token: %s", string(cbRaw))
	}
	for _, c := range cbResp.Cookies() {
		if strings.Contains(c.Value, "opshub_at_") {
			t.Fatalf("callback cookie %s leaked raw bearer token", c.Name)
		}
	}
}

func assertZeroTokenSubstringsInDir(t *testing.T, rootDir string) {
	t.Helper()
	tokenPrefixes := []string{"opshub_at_", "ya29.", "gho_", "ghu_"}
	err := filepath.WalkDir(rootDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		s := string(raw)
		for _, pfx := range tokenPrefixes {
			if strings.Contains(s, pfx) {
				t.Errorf("workspace file %s leaked bearer token prefix %q", p, pfx)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s): %v", rootDir, err)
	}
}

func TestModule05OAuth3LOPKCEConsentDualAttributionAndZeroActorTokens(t *testing.T) {
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
		DataDir:       t.TempDir(),
		SimNamespace:  "prod-sim",
		LedgerAPIURL:  ledgerTS.URL,
		Require3LO:    true,
		OAuthClientID: "sre-gateway-3lo",
	})
	if err != nil {
		t.Fatalf("opshub.NewServer: %v", err)
	}
	opsTS := httptest.NewServer(ops.Handler())
	t.Cleanup(opsTS.Close)

	fakeATE := atetest.NewFakeControl()
	ateAddr, caFile, tokenFile := atetest.StartServer(t, fakeATE)

	gwLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen broker: %v", err)
	}
	brokerURL := "http://" + gwLis.Addr().String()

	certDir := t.TempDir()
	wantAgentSPIFFE := "spiffe://agents.global.org-998877.system.id.goog/resources/container/projects/112233/locations/us-central1-a/clusters/ate-cluster/ns/workshop-301/sa/sre-gateway"
	svidFile := writeSPIFFEBundlePEM(t, certDir, "x509.credential-bundle.private-key.pem", wantAgentSPIFFE, time.Now().UTC().Add(12*time.Hour))

	actorWorkspaceDir := t.TempDir()
	var (
		actorTS         *httptest.Server
		routerWakeCalls atomic.Int64
		wireInspectMu   sync.Mutex
		actorWireBlobs  []string
	)
	recordWireBlob := func(blob string) {
		wireInspectMu.Lock()
		actorWireBlobs = append(actorWireBlobs, blob)
		wireInspectMu.Unlock()
	}

	getActor := func() *httptest.Server {
		if actorTS != nil {
			return actorTS
		}
		srv, err := agentruntime.NewServer(agentruntime.Config{
			WorkspaceDir: actorWorkspaceDir,
			BrokerURL:    brokerURL,
			ModelName:    "gemini-3.8-flash",
			TemplateName: "sre-agent-v4",
			BuildRunner:  buildModule04Runner,
		})
		if err != nil {
			t.Fatalf("agentruntime.NewServer: %v", err)
		}
		actorTS = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var hdrBuf strings.Builder
			for k, vs := range r.Header {
				hdrBuf.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
			}
			rawReq, _ := io.ReadAll(r.Body)
			recordWireBlob(hdrBuf.String() + "\n" + string(rawReq))
			r.Body = io.NopCloser(bytes.NewReader(rawReq))
			srv.Handler().ServeHTTP(w, r)
		}))
		t.Cleanup(actorTS.Close)
		return actorTS
	}

	routerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routerWakeCalls.Add(1)
		fakeATE.MarkRunning("workshop-301", "inc-402", "sre-workers-0")
		target := getActor()
		fwdReq, _ := http.NewRequestWithContext(r.Context(), r.Method, target.URL+r.URL.RequestURI(), r.Body)
		fwdReq.Header = r.Header.Clone()
		resp, err := target.Client().Do(fwdReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		rawResp, _ := io.ReadAll(resp.Body)
		recordWireBlob(string(rawResp))
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(rawResp)
	}))
	t.Cleanup(routerTS.Close)

	gwSrv, err := broker.NewServer(broker.Config{
		Namespace:             "workshop-301",
		Atespace:              "workshop-301",
		SimNamespace:          "prod-sim",
		ProjectID:             "demo-project",
		Region:                "us-central1",
		ModelLocation:         "global",
		GeminiModel:           "gemini-3.8-flash",
		MockLLM:               true,
		DefaultTemplate:       "sre-agent-v4",
		SelfURL:               brokerURL,
		RouterURL:             routerTS.URL,
		OpsHubURL:             opsTS.URL,
		LedgerAPIURL:          ledgerTS.URL,
		IdleAfter:             10 * time.Minute,
		AgentIdentityCertFile: svidFile,
		GovernanceEnforced:    true,
		Require3LO:            true,
		OAuthClientID:         "sre-gateway-3lo",
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

	// 1. Stage INC-402 remediation so inc-402 suspends at 0 vCPU at the Remediation Gate.
	stageBody, _ := json.Marshal(broker.ChatRequest{
		IncidentID: "INC-402",
		Template:   "sre-agent-v4",
		Prompt:     "Stage and remediate INC-402 end-to-end: query MCP logs and health, patch config/pool.yaml and pool/store.go on branch fix/inc-402-pool-leak, verify with ledger-verify, push the branch, and apply_remediation_pr.",
	})
	stageResp, err := http.Post(brokerURL+"/api/chat", "application/json", bytes.NewReader(stageBody))
	if err != nil {
		t.Fatalf("POST /api/chat: %v", err)
	}
	var gated broker.ChatResponse
	_ = json.NewDecoder(stageResp.Body).Decode(&gated)
	stageResp.Body.Close()
	if !gated.RemediationGatePending || gated.CallID == "" {
		t.Fatalf("expected RemediationGatePending=true, got %+v", gated)
	}
	if a := fakeATE.Actor("workshop-301", "inc-402"); a == nil || a.GetStatus().GetState() != ateapipb.ActorState_ACTOR_STATE_SUSPENDED {
		t.Fatalf("expected inc-402 SUSPENDED, got %v", a)
	}

	// 2. Approving Remediation Gate without a 3LO grant must return HTTP 401 consent_required
	// without waking inc-402 or consuming the pending gate.
	wakesBeforeUnauthApprove := routerWakeCalls.Load()
	unauthAppResp, err := http.Post(brokerURL+"/api/incidents/INC-402/remediation/approve", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("unauthenticated approve: %v", err)
	}
	unauthAppRaw, _ := io.ReadAll(unauthAppResp.Body)
	unauthAppResp.Body.Close()
	if unauthAppResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("approve without 3LO status = %d (%s), want 401 Unauthorized", unauthAppResp.StatusCode, string(unauthAppRaw))
	}
	var consentDoc struct {
		ConsentRequired bool   `json:"consent_required"`
		AuthURL         string `json:"auth_url"`
		Provider        string `json:"provider"`
	}
	if err := json.Unmarshal(unauthAppRaw, &consentDoc); err != nil {
		t.Fatalf("decode consent_required JSON: %v", err)
	}
	if !consentDoc.ConsentRequired || consentDoc.AuthURL == "" || consentDoc.Provider == "" {
		t.Fatalf("consentDoc = %+v, want consent_required=true with auth_url and provider", consentDoc)
	}
	if routerWakeCalls.Load() != wakesBeforeUnauthApprove {
		t.Errorf("unauthenticated approve woke inc-402 (%d -> %d); actor must stay at 0 vCPU until 3LO is granted", wakesBeforeUnauthApprove, routerWakeCalls.Load())
	}

	// 3. Complete real OAuth 2.0 + S256 PKCE consent via the returned auth_url.
	completeBrowserPKCEFlow(t, consentDoc.AuthURL)

	// 4. Approve Remediation Gate now that 3LO grant is active -> wakes inc-402, injects
	// Bearer token + SPIFFE principal on sre-gateway -> ops-hub hop, merges PR-1, and restores ledger-api.
	appResp, err := http.Post(brokerURL+"/api/incidents/INC-402/remediation/approve", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("authenticated approve: %v", err)
	}
	appRaw, _ := io.ReadAll(appResp.Body)
	appResp.Body.Close()
	if appResp.StatusCode != http.StatusOK {
		t.Fatalf("approve with 3LO status = %d (%s), want 200 OK", appResp.StatusCode, string(appRaw))
	}

	// 5. Verify dual operator + agent attribution on ops-hub /api/pulls and /api/governance!
	pullsResp, err := http.Get(opsTS.URL + "/api/pulls")
	if err != nil {
		t.Fatalf("GET /api/pulls: %v", err)
	}
	var pulls []opshub.PullRequest
	_ = json.NewDecoder(pullsResp.Body).Decode(&pulls)
	pullsResp.Body.Close()
	if len(pulls) != 1 {
		t.Fatalf("expected 1 merged PR on ops-hub, got %d (%+v)", len(pulls), pulls)
	}
	if pulls[0].OperatorSub != "sre-operator" {
		t.Errorf("PR OperatorSub = %q, want sre-operator", pulls[0].OperatorSub)
	}
	if pulls[0].AgentIdentity != wantAgentSPIFFE {
		t.Errorf("PR AgentIdentity = %q, want %q", pulls[0].AgentIdentity, wantAgentSPIFFE)
	}
	if !pulls[0].AgenticDelegation {
		t.Errorf("PR AgenticDelegation = false, want true")
	}

	hzResp, err := http.Get(ledgerTS.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	hzResp.Body.Close()
	if hzResp.StatusCode != http.StatusOK {
		t.Errorf("ledger-api /healthz after 3LO remediation = %d, want 200 OK", hzResp.StatusCode)
	}

	// 6. Prove ZERO bearer tokens (opshub_at_, ya29., gho_) ever entered agentruntime HTTP traffic,
	// process environment, or /workspace disk, or leaked via GET /api/governance!
	wireInspectMu.Lock()
	blobsCopy := append([]string(nil), actorWireBlobs...)
	wireInspectMu.Unlock()
	for i, blob := range blobsCopy {
		for _, pfx := range []string{"opshub_at_", "ya29.", "gho_", "Authorization: Bearer"} {
			if strings.Contains(blob, pfx) {
				t.Errorf("actor wire message #%d leaked forbidden credential marker %q:\n%s", i, pfx, blob)
			}
		}
	}
	assertZeroTokenSubstringsInDir(t, actorWorkspaceDir)
	for _, envKV := range os.Environ() {
		if strings.Contains(envKV, "opshub_at_") {
			t.Errorf("process environment leaked opshub_at_ token: %s", envKV)
		}
	}

	govResp, err := http.Get(brokerURL + "/api/governance")
	if err != nil {
		t.Fatal(err)
	}
	govRaw, _ := io.ReadAll(govResp.Body)
	govResp.Body.Close()
	if strings.Contains(string(govRaw), "opshub_at_") {
		t.Fatalf("/api/governance leaked raw bearer token: %s", string(govRaw))
	}
	var govDoc struct {
		OAuth3LO broker.OAuthStatus `json:"oauth_3lo"`
	}
	_ = json.Unmarshal(govRaw, &govDoc)
	if !govDoc.OAuth3LO.Granted || govDoc.OAuth3LO.OperatorSub != "sre-operator" || govDoc.OAuth3LO.TTLRemainingSeconds <= 0 {
		t.Errorf("OAuth3LO status = %+v, want Granted=true OperatorSub=sre-operator TTLRemainingSeconds>0", govDoc.OAuth3LO)
	}

	// Verify read-only GET /ops/api/pulls proxies dual attribution cleanly while POST /ops/api/pulls stays denied.
	pullsProxyResp, err := http.Get(brokerURL + "/ops/api/pulls")
	if err != nil {
		t.Fatalf("GET /ops/api/pulls: %v", err)
	}
	pullsProxyRaw, _ := io.ReadAll(pullsProxyResp.Body)
	pullsProxyResp.Body.Close()
	if pullsProxyResp.StatusCode != http.StatusOK || !strings.Contains(string(pullsProxyRaw), `"agentic_delegation":true`) {
		t.Fatalf("GET /ops/api/pulls = %d (%s), want 200 with agentic_delegation=true", pullsProxyResp.StatusCode, string(pullsProxyRaw))
	}
	pullsPostResp, err := http.Post(brokerURL+"/ops/api/pulls", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /ops/api/pulls: %v", err)
	}
	pullsPostResp.Body.Close()
	if pullsPostResp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST /ops/api/pulls = %d, want 403 Forbidden", pullsPostResp.StatusCode)
	}

	// 7. Verify POST /api/oauth/revoke clears the grant immediately, and test 1s TTL expiry (auth.ExpirySkew).
	revResp, err := http.Post(brokerURL+"/api/oauth/revoke", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/oauth/revoke: %v", err)
	}
	revResp.Body.Close()
	if revResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/oauth/revoke = %d, want 200", revResp.StatusCode)
	}

	startShortResp, err := http.Post(brokerURL+"/api/oauth/start", "application/json", strings.NewReader(`{"ttl_seconds":1}`))
	if err != nil {
		t.Fatalf("POST /api/oauth/start (ttl=1s): %v", err)
	}
	var startShort struct {
		AuthURL string `json:"auth_url"`
	}
	_ = json.NewDecoder(startShortResp.Body).Decode(&startShort)
	startShortResp.Body.Close()
	completeBrowserPKCEFlow(t, startShort.AuthURL)

	// Immediately after grant (< 1s), grant must be readable despite auth.InMemoryCredentialStore's 10s ExpirySkew!
	govImmediate, _ := http.Get(brokerURL + "/api/governance")
	var govImmDoc struct {
		OAuth3LO broker.OAuthStatus `json:"oauth_3lo"`
	}
	_ = json.NewDecoder(govImmediate.Body).Decode(&govImmDoc)
	govImmediate.Body.Close()
	if !govImmDoc.OAuth3LO.Granted {
		t.Fatalf("1s TTL grant was treated as immediately expired (auth.ExpirySkew not offset): %+v", govImmDoc.OAuth3LO)
	}

	time.Sleep(1200 * time.Millisecond)
	govExpired, _ := http.Get(brokerURL + "/api/governance")
	var govExpDoc struct {
		OAuth3LO broker.OAuthStatus `json:"oauth_3lo"`
	}
	_ = json.NewDecoder(govExpired.Body).Decode(&govExpDoc)
	govExpired.Body.Close()
	if govExpDoc.OAuth3LO.Granted {
		t.Errorf("1s TTL grant remained active after 1.2s: %+v", govExpDoc.OAuth3LO)
	}
}

func TestModule05GCPAuthManagerRetrieveAndFinalizeFlow(t *testing.T) {
	var finalized atomic.Bool
	var retrieveCalls atomic.Int64
	var finalizeCalls atomic.Int64

	authIdentityTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/projects/112233/locations/us-central1/authProviders/ops-hub-3lo/credentials:retrieve":
			retrieveCalls.Add(1)
			var reqBody struct {
				UserID      string   `json:"userId"`
				Scopes      []string `json:"scopes"`
				ContinueURI string   `json:"continueUri"`
			}
			_ = json.NewDecoder(r.Body).Decode(&reqBody)
			if !finalized.Load() {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"uriConsentRequired": map[string]any{
						"authorizationUri": reqBody.ContinueURI + "?consent_nonce=gcp-nonce-777",
						"consentNonce":     "gcp-nonce-777",
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": map[string]any{
					"header":     "Authorization",
					"token":      "Bearer opshub_at_managed_gcp_auth_provider_token",
					"expireTime": time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339),
				},
			})

		case "/v1/projects/112233/locations/us-central1/authProviders/ops-hub-3lo/credentials:finalize":
			finalizeCalls.Add(1)
			var finBody struct {
				UserID                string `json:"userId"`
				UserIDValidationState string `json:"userIdValidationState"`
				ConsentNonce          string `json:"consentNonce"`
			}
			_ = json.NewDecoder(r.Body).Decode(&finBody)
			if finBody.ConsentNonce != "gcp-nonce-777" || finBody.UserID == "" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid finalize payload"})
				return
			}
			finalized.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "FINALIZED"})

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(authIdentityTS.Close)

	srv, err := broker.NewServer(broker.Config{
		Namespace:               "workshop-301",
		Atespace:                "workshop-301",
		SimNamespace:            "prod-sim",
		ProjectID:               "demo-project",
		Region:                  "us-central1",
		MockLLM:                 true,
		GovernanceEnforced:      true,
		Require3LO:              true,
		GCPAuthProviderID:       "projects/112233/locations/us-central1/authProviders/ops-hub-3lo",
		AuthIdentityUpstreamURL: authIdentityTS.URL,
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	gwTS := httptest.NewServer(srv.Handler())
	t.Cleanup(gwTS.Close)

	// 1. Start 3LO flow -> hits adk/v2/auth/gcp RetrieveCredential and returns uriConsentRequired.
	startResp, err := http.Post(gwTS.URL+"/api/oauth/start", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/oauth/start: %v", err)
	}
	var startDoc struct {
		ConsentRequired bool   `json:"consent_required"`
		AuthURL         string `json:"auth_url"`
		Provider        string `json:"provider"`
	}
	_ = json.NewDecoder(startResp.Body).Decode(&startDoc)
	startResp.Body.Close()
	if !startDoc.ConsentRequired || startDoc.Provider != "agent-identity-auth-manager" || !strings.Contains(startDoc.AuthURL, "consent_nonce=gcp-nonce-777") {
		t.Fatalf("startDoc = %+v, want consent_required=true provider=agent-identity-auth-manager", startDoc)
	}

	// 2. Hit ContinueURI (/api/oauth/callback?consent_nonce=gcp-nonce-777) -> finalizes and retrieves credential.
	cbReq, _ := http.NewRequest(http.MethodGet, startDoc.AuthURL, nil)
	cbReq.Header.Set("Accept", "application/json")
	cbResp, err := http.DefaultClient.Do(cbReq)
	if err != nil {
		t.Fatalf("GET ContinueURI: %v", err)
	}
	cbRaw, _ := io.ReadAll(cbResp.Body)
	cbResp.Body.Close()
	if cbResp.StatusCode != http.StatusOK {
		t.Fatalf("GET ContinueURI status = %d (%s), want 200", cbResp.StatusCode, string(cbRaw))
	}
	if finalizeCalls.Load() != 1 || retrieveCalls.Load() < 2 {
		t.Errorf("finalizeCalls=%d retrieveCalls=%d, want finalizeCalls=1 retrieveCalls>=2", finalizeCalls.Load(), retrieveCalls.Load())
	}

	// 3. Verify /api/governance shows granted=true via agent-identity-auth-manager without exposing raw token.
	govResp, err := http.Get(gwTS.URL + "/api/governance")
	if err != nil {
		t.Fatal(err)
	}
	govRaw, _ := io.ReadAll(govResp.Body)
	govResp.Body.Close()
	if strings.Contains(string(govRaw), "opshub_at_") {
		t.Fatalf("/api/governance leaked managed token: %s", string(govRaw))
	}
	var govDoc struct {
		OAuth3LO broker.OAuthStatus `json:"oauth_3lo"`
	}
	_ = json.Unmarshal(govRaw, &govDoc)
	if !govDoc.OAuth3LO.Granted || govDoc.OAuth3LO.AuthMethod != "agent-identity-auth-manager" {
		t.Errorf("OAuth3LO = %+v, want Granted=true AuthMethod=agent-identity-auth-manager", govDoc.OAuth3LO)
	}
}

func TestModule05GovernanceConsoleAndArchitectureParity(t *testing.T) {
	certDir := t.TempDir()
	wantSPIFFE := "spiffe://agents.global.org-998877.system.id.goog/resources/container/projects/112233/locations/us-central1-a/clusters/ate-cluster/ns/workshop-301/sa/sre-gateway"
	svidFile := writeSPIFFEBundlePEM(t, certDir, "x509.credential-bundle.private-key.pem", wantSPIFFE, time.Now().UTC().Add(12*time.Hour))

	srv, err := broker.NewServer(broker.Config{
		Namespace:             "workshop-301",
		Atespace:              "workshop-301",
		SimNamespace:          "prod-sim",
		ProjectID:             "demo-project",
		Region:                "us-central1",
		MockLLM:               true,
		DefaultTemplate:       "sre-agent-v4",
		AgentIdentityCertFile: svidFile,
		GovernanceEnforced:    true,
		ModelArmorTemplateID:  "sre-triage-guard",
		Require3LO:            true,
		GCPAuthProviderID:     "projects/112233/locations/us-central1/authProviders/ops-hub-3lo",
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// 1. Verify Incident Console HTML serves Governance & policy log posture grids + 3LO controls.
	rootResp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	htmlBytes, _ := io.ReadAll(rootResp.Body)
	rootResp.Body.Close()
	htmlStr := string(htmlBytes)
	for _, elemID := range []string{
		"gov-identity-grid",
		"gov-armor-grid",
		"gov-oauth-grid",
		"btn-authorize-3lo",
		"cmdk-oauth-box",
	} {
		if !strings.Contains(htmlStr, elemID) {
			t.Errorf("GET / HTML missing Module 05 UI element %q", elemID)
		}
	}

	// 2. Verify /api/architecture reflects live SPIFFE SVID, Model Armor template, and 3LO Auth Manager.
	archResp, err := http.Get(ts.URL + "/api/architecture")
	if err != nil {
		t.Fatalf("GET /api/architecture: %v", err)
	}
	archRaw, _ := io.ReadAll(archResp.Body)
	archResp.Body.Close()
	archStr := string(archRaw)
	for _, want := range []string{
		"agents.global.org-998877.system.id.goog",
		"sre-triage-guard",
		"ops-hub-3lo",
		"GCPAuthzPolicy",
	} {
		if !strings.Contains(archStr, want) {
			t.Errorf("/api/architecture missing Module 05 live governance detail %q", want)
		}
	}
}

func TestModule05DynamicAgentRegistryAnnotationsAndDirectRemediationBypass403(t *testing.T) {
	var upstreamDestructiveCalls atomic.Int64
	var upstreamReadOnlyCalls atomic.Int64

	cloudUpstreamMCP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq struct {
			ID     any `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&rpcReq)
		if rpcReq.Params.Name == "read_canary_metrics" {
			upstreamReadOnlyCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      rpcReq.ID,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": `{"canary":"ok"}`}},
					"isError": false,
				},
			})
			return
		}
		upstreamDestructiveCalls.Add(1)
		http.Error(w, "destructive tool should have been blocked at broker", http.StatusInternalServerError)
	}))
	t.Cleanup(cloudUpstreamMCP.Close)

	cloudRegistryTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/projects/demo-project/locations/us-central1/mcpServers":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mcpServers": []map[string]any{
					{
						"name":        "projects/demo-project/locations/us-central1/mcpServers/cloud-chaos-mcp",
						"mcpServerId": "cloud-chaos-mcp",
						"displayName": "Cloud Chaos & Canary MCP",
						"description": "Dynamically registered MCP server in Agent Registry",
						"interfaces": []map[string]any{
							{"url": cloudUpstreamMCP.URL, "protocolBinding": "JSONRPC"},
						},
						"tools": []map[string]any{
							{
								"name":        "read_canary_metrics",
								"description": "Read-only canary health metrics",
								"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false},
							},
							{
								"name":        "purge_canary_tables",
								"description": "Arbitrary newly registered destructiveHint=true tool",
								"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true},
							},
							{
								"name":        "unannotated_mystery_tool",
								"description": "Tool omitting annotations; defaults destructiveHint=true per adk/v2/agentregistry/types.go",
							},
						},
					},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	t.Cleanup(cloudRegistryTS.Close)

	genaiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(genaiStub.Close)

	ledger := ledgersvc.NewServer(ledgersvc.Options{
		Config:    ledgersvc.RuntimeConfig{MaxOpenConns: 2, IdleTimeoutSeconds: 0, AcquireTimeoutMs: 60},
		LogOutput: io.Discard,
	})
	t.Cleanup(ledger.Close)
	ledgerTS := httptest.NewServer(ledger.Handler())
	t.Cleanup(ledgerTS.Close)

	ops, err := opshub.NewServer(opshub.Options{
		DataDir:       t.TempDir(),
		SimNamespace:  "prod-sim",
		LedgerAPIURL:  ledgerTS.URL,
		Require3LO:    true,
		OAuthClientID: "sre-gateway-3lo",
	})
	if err != nil {
		t.Fatalf("opshub.NewServer: %v", err)
	}
	opsTS := httptest.NewServer(ops.Handler())
	t.Cleanup(opsTS.Close)

	gwSrv, err := broker.NewServer(broker.Config{
		Namespace:                "workshop-301",
		Atespace:                 "workshop-301",
		SimNamespace:             "prod-sim",
		ProjectID:                "demo-project",
		Region:                   "us-central1",
		MockLLM:                  false,
		GenAIUpstreamURL:         genaiStub.URL,
		SkillRegistryUpstreamURL: cloudRegistryTS.URL,
		AgentRegistryUpstreamURL: cloudRegistryTS.URL,
		CloudTraceUpstreamURL:    genaiStub.URL,
		OpsHubURL:                opsTS.URL,
		LedgerAPIURL:             ledgerTS.URL,
		GovernanceEnforced:       true,
		Require3LO:               true,
		OAuthClientID:            "sre-gateway-3lo",
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = gwSrv.Close() })
	gwTS := httptest.NewServer(gwSrv.Handler())
	t.Cleanup(gwTS.Close)

	callMCP := func(serverID, toolName string, args map[string]any, extraHeaders map[string]string) int {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      toolName,
				"arguments": args,
			},
		})
		req, _ := http.NewRequest(http.MethodPost, gwTS.URL+"/mcp/"+serverID, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /mcp/%s (%s): %v", serverID, toolName, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// (a1) Newly registered readOnlyHint=true, destructiveHint=false tool is allowed and proxied.
	if code := callMCP("cloud-chaos-mcp", "read_canary_metrics", map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("read_canary_metrics status = %d, want 200 OK", code)
	}
	if upstreamReadOnlyCalls.Load() != 1 {
		t.Errorf("upstreamReadOnlyCalls = %d, want 1", upstreamReadOnlyCalls.Load())
	}

	// (a2) Arbitrary newly registered destructiveHint=true tool in Agent Registry is automatically rejected with 403.
	if code := callMCP("cloud-chaos-mcp", "purge_canary_tables", map[string]any{"force": true}, nil); code != http.StatusForbidden {
		t.Fatalf("purge_canary_tables status = %d, want 403 Forbidden", code)
	}
	// (a3) Unannotated tool defaults destructiveHint=true per adk/v2/agentregistry/types.go and is rejected with 403.
	if code := callMCP("cloud-chaos-mcp", "unannotated_mystery_tool", map[string]any{}, nil); code != http.StatusForbidden {
		t.Fatalf("unannotated_mystery_tool status = %d, want 403 Forbidden", code)
	}
	if upstreamDestructiveCalls.Load() != 0 {
		t.Fatalf("upstreamDestructiveCalls = %d, want 0 (blocked at Governed Broker)", upstreamDestructiveCalls.Load())
	}

	// (b) Calling /mcp/ops-hub-mcp apply_remediation_pr directly WITHOUT human Remediation Gate approval
	// (even with an active 3LO grant and X-Remediation-Gate-Confirmed: true) must return 403!
	startResp, err := http.Post(gwTS.URL+"/api/oauth/start", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/oauth/start: %v", err)
	}
	var startDoc struct {
		AuthURL string `json:"auth_url"`
	}
	_ = json.NewDecoder(startResp.Body).Decode(&startDoc)
	startResp.Body.Close()
	completeBrowserPKCEFlow(t, startDoc.AuthURL)

	bypassCode := callMCP("ops-hub-mcp", "apply_remediation_pr", map[string]any{
		"incident_id": "INC-402",
		"branch":      "fix/inc-402-pool-leak",
		"title":       "Direct unconfirmed bypass attempt",
	}, map[string]string{
		"X-Remediation-Gate-Confirmed": "true",
		"X-Remediation-Gate-Token":     "forged-invalid-token",
	})
	if bypassCode != http.StatusForbidden {
		t.Fatalf("direct unconfirmed apply_remediation_pr status = %d, want 403 Forbidden", bypassCode)
	}

	pullsResp, err := http.Get(opsTS.URL + "/api/pulls")
	if err != nil {
		t.Fatal(err)
	}
	var pulls []opshub.PullRequest
	_ = json.NewDecoder(pullsResp.Body).Decode(&pulls)
	pullsResp.Body.Close()
	if len(pulls) != 0 {
		t.Fatalf("expected 0 merged PRs after blocked bypass attempt, got %+v", pulls)
	}

	// Verify /api/governance recorded DENIED_DESTRUCTIVE_MCP verdicts for all three blocked calls.
	govResp, err := http.Get(gwTS.URL + "/api/governance")
	if err != nil {
		t.Fatal(err)
	}
	var govDoc struct {
		MCPPolicy broker.MCPPolicyStatus `json:"mcp_policy"`
		Events    []broker.PolicyEvent   `json:"events"`
	}
	_ = json.NewDecoder(govResp.Body).Decode(&govDoc)
	govResp.Body.Close()
	if govDoc.MCPPolicy.DeniedCount < 3 {
		t.Errorf("MCPPolicy.DeniedCount = %d, want >= 3", govDoc.MCPPolicy.DeniedCount)
	}
	deniedVerdicts := 0
	for _, ev := range govDoc.Events {
		if ev.Verdict == "DENIED_DESTRUCTIVE_MCP" {
			deniedVerdicts++
		}
	}
	if deniedVerdicts < 3 {
		t.Errorf("saw %d DENIED_DESTRUCTIVE_MCP policy events, want >= 3 (events=%+v)", deniedVerdicts, govDoc.Events)
	}
}
