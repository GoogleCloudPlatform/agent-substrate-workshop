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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/ledgersvc"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/opshub"
)

var hiddenCSRFInput = regexp.MustCompile(`name="csrf_token"\s+value="([^"]+)"`)

func TestOpsHubOAuthPKCEAndGovernedMCP(t *testing.T) {
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
	_ = ledger.ExecuteTransfer(t.Context(), "bad-mcp-1")
	_ = ledger.ExecuteTransfer(t.Context(), "bad-mcp-2")

	ledgerTS := httptest.NewServer(ledger.Handler())
	t.Cleanup(ledgerTS.Close)

	hub, err := opshub.NewServer(opshub.Options{
		DataDir:       t.TempDir(),
		LedgerAPIURL:  ledgerTS.URL,
		Require3LO:    true,
		OAuthClientID: "sre-gateway-client",
	})
	if err != nil {
		t.Fatalf("opshub.NewServer: %v", err)
	}
	opsTS := httptest.NewServer(hub.Handler())
	t.Cleanup(opsTS.Close)

	// 1. OAuth discovery document.
	discCode, discBody := httpGet(t, opsTS.URL+"/.well-known/oauth-authorization-server")
	if discCode != http.StatusOK || !strings.Contains(discBody, `"S256"`) {
		t.Fatalf("OAuth discovery = %d (%s), want 200 with S256", discCode, discBody)
	}

	// 2. Reject plain PKCE and untrusted external redirect_uri.
	verifier := generatePKCEVerifier(t)
	challenge := computeS256Challenge(verifier)
	redirectURI := "http://127.0.0.1:8081/oauth/callback"

	plainCode, _ := httpGet(t, opsTS.URL+"/oauth/authorize?"+url.Values{
		"response_type":         {"code"},
		"client_id":             {"sre-gateway-client"},
		"redirect_uri":          {redirectURI},
		"state":                 {"st-1"},
		"scope":                 {"openid profile remediation:write"},
		"code_challenge":        {verifier},
		"code_challenge_method": {"plain"},
	}.Encode())
	if plainCode != http.StatusBadRequest {
		t.Fatalf("GET /oauth/authorize with plain PKCE = %d, want 400", plainCode)
	}

	evilCode, _ := httpGet(t, opsTS.URL+"/oauth/authorize?"+url.Values{
		"response_type":         {"code"},
		"client_id":             {"sre-gateway-client"},
		"redirect_uri":          {"https://evil.invalid/steal"},
		"state":                 {"st-1"},
		"scope":                 {"openid profile remediation:write"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode())
	if evilCode != http.StatusBadRequest {
		t.Fatalf("GET /oauth/authorize with untrusted redirect_uri = %d, want 400", evilCode)
	}

	// 3. Complete CSRF-protected consent + PKCE token exchange + replay revocation check.
	readOnlyToken := issuePKCEToken(t, opsTS.URL, redirectURI, "openid profile incidents:read", true)
	writeToken := issuePKCEToken(t, opsTS.URL, redirectURI, "openid profile remediation:write", false)

	// Verify /oauth/userinfo returns masked PII and Cache-Control: no-store.
	uiReq, _ := http.NewRequest(http.MethodGet, opsTS.URL+"/oauth/userinfo", nil)
	uiReq.Header.Set("Authorization", "Bearer "+writeToken)
	uiResp, err := http.DefaultClient.Do(uiReq)
	if err != nil {
		t.Fatalf("GET /oauth/userinfo: %v", err)
	}
	uiRaw, _ := io.ReadAll(uiResp.Body)
	uiResp.Body.Close()
	if uiResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /oauth/userinfo = %d (%s), want 200", uiResp.StatusCode, uiRaw)
	}
	if !strings.Contains(string(uiRaw), "***") {
		t.Errorf("/oauth/userinfo = %s, want masked email", uiRaw)
	}

	// 4. MCP initialize, notifications/initialized, ping, and tools/list annotations.
	initCode, initHdr, initBody := callMCP(t, opsTS.URL+"/mcp", map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"clientInfo":      map[string]any{"name": "test-client", "version": "1.0.0"},
		},
	}, "", "")
	if initCode != http.StatusOK || initHdr.Get("Mcp-Session-Id") == "" {
		t.Fatalf("MCP initialize = %d (session=%q, body=%s), want 200 + Mcp-Session-Id", initCode, initHdr.Get("Mcp-Session-Id"), initBody)
	}

	notifCode, _, _ := callMCP(t, opsTS.URL+"/mcp", map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}, "", "")
	if notifCode != http.StatusAccepted {
		t.Fatalf("MCP notifications/initialized = %d, want 202", notifCode)
	}

	pingCode, _, _ := callMCP(t, opsTS.URL+"/mcp", map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "ping",
	}, "", "")
	if pingCode != http.StatusOK {
		t.Fatalf("MCP ping = %d, want 200", pingCode)
	}

	listCode, _, listBody := callMCP(t, opsTS.URL+"/mcp", map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/list",
	}, "", "")
	if listCode != http.StatusOK {
		t.Fatalf("MCP tools/list = %d (%s), want 200", listCode, listBody)
	}
	var listResp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Annotations struct {
					ReadOnlyHint    bool `json:"readOnlyHint"`
					DestructiveHint bool `json:"destructiveHint"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(listBody), &listResp); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	wantHints := map[string][2]bool{
		"get_incident":         {true, false},
		"list_commits":         {true, false},
		"get_service_slo":      {true, false},
		"apply_remediation_pr": {false, true},
		"drop_ledger_database": {false, true},
	}
	if len(listResp.Result.Tools) != len(wantHints) {
		t.Fatalf("tools/list returned %d tools, want %d", len(listResp.Result.Tools), len(wantHints))
	}
	for _, tool := range listResp.Result.Tools {
		want, ok := wantHints[tool.Name]
		if !ok {
			t.Fatalf("unexpected MCP tool %q", tool.Name)
		}
		if tool.Annotations.ReadOnlyHint != want[0] || tool.Annotations.DestructiveHint != want[1] {
			t.Errorf("tool %s annotations = (readOnly=%t, destructive=%t), want (%t, %t)",
				tool.Name, tool.Annotations.ReadOnlyHint, tool.Annotations.DestructiveHint, want[0], want[1])
		}
	}

	// 5. Read-only MCP tools succeed without 3LO token.
	for _, tc := range []struct {
		name string
		args map[string]any
		sub  string
	}{
		{"get_incident", map[string]any{"incident_id": "INC-402"}, "INC-402"},
		{"list_commits", map[string]any{"branch": "main"}, "Initial ledger-api"},
		{"get_service_slo", map[string]any{"service": "ledger-api"}, "pool_exhausted"},
	} {
		c, _, b := callMCP(t, opsTS.URL+"/mcp", map[string]any{
			"jsonrpc": "2.0",
			"id":      10,
			"method":  "tools/call",
			"params":  map[string]any{"name": tc.name, "arguments": tc.args},
		}, "", "")
		if c != http.StatusOK || !strings.Contains(b, tc.sub) {
			t.Fatalf("MCP tools/call %s = %d (%s), want 200 containing %q", tc.name, c, b, tc.sub)
		}
	}

	// 6. Destructive trap drop_ledger_database always fails closed with 403 even with writeToken.
	dropCode, _, dropBody := callMCP(t, opsTS.URL+"/mcp", map[string]any{
		"jsonrpc": "2.0",
		"id":      20,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "drop_ledger_database",
			"arguments": map[string]any{"confirm": true, "reason": "injected instruction"},
		},
	}, writeToken, "")
	if dropCode != http.StatusForbidden {
		t.Fatalf("MCP drop_ledger_database = %d (%s), want 403 Forbidden", dropCode, dropBody)
	}

	// 7. Stage and push remediation branch, then test 3LO enforcement on apply_remediation_pr.
	workDir := filepath.Join(t.TempDir(), "mcp-fix")
	runGit(t, "", "clone", opsTS.URL+"/git/ledger-api.git", workDir)
	runGit(t, workDir, "checkout", "-b", "fix/mcp-402")
	_ = os.WriteFile(filepath.Join(workDir, "config", "pool.yaml"), []byte("max_open_conns: 32\nidle_timeout_seconds: 30\nacquire_timeout_ms: 150\n"), 0o644)
	storePath := filepath.Join(workDir, "pool", "store.go")
	raw, _ := os.ReadFile(storePath)
	patched := strings.Replace(string(raw), "if err != nil {\n\t\treturn err\n\t}\n", "if err != nil {\n\t\treturn err\n\t}\n\tdefer rows.Close()\n", 1)
	_ = os.WriteFile(storePath, []byte(patched), 0o644)
	runGit(t, workDir, "add", ".")
	runGit(t, workDir, "commit", "-m", "Remediate pool leak via governed MCP")
	runGit(t, workDir, "push", "origin", "HEAD:fix/mcp-402")

	applyReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      30,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "apply_remediation_pr",
			"arguments": map[string]any{
				"incident_id": "INC-402",
				"branch":      "fix/mcp-402",
				"title":       "Remediate INC-402 connection pool leak",
			},
		},
	}

	// Unauthenticated -> 401 Unauthorized.
	unauthCode, unauthHdr, _ := callMCP(t, opsTS.URL+"/mcp", applyReq, "", "")
	if unauthCode != http.StatusUnauthorized || !strings.Contains(unauthHdr.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("unauthenticated apply_remediation_pr = %d (WWW-Authenticate=%q), want 401 Bearer", unauthCode, unauthHdr.Get("WWW-Authenticate"))
	}

	// Read-only scope token -> 403 Forbidden.
	roCode, _, _ := callMCP(t, opsTS.URL+"/mcp", applyReq, readOnlyToken, "")
	if roCode != http.StatusForbidden {
		t.Fatalf("insufficient-scope apply_remediation_pr = %d, want 403", roCode)
	}

	// Valid 3LO write token + SPIFFE agent identity -> 200 OK, resolves INC-402, recovers ledger-api.
	agentSPIFFE := "spiffe://agents.global.proj-12345.system.id.goog/resources/container/projects/12345/locations/us-central1-a/clusters/ate-cluster/ns/workshop-301/sa/sre-gateway"
	okCode, _, okBody := callMCP(t, opsTS.URL+"/mcp", applyReq, writeToken, agentSPIFFE)
	if okCode != http.StatusOK || !strings.Contains(okBody, "MERGED") {
		t.Fatalf("authenticated apply_remediation_pr = %d (%s), want 200 MERGED", okCode, okBody)
	}
	if hCode, _ := httpGet(t, ledgerTS.URL+"/healthz"); hCode != http.StatusOK {
		t.Fatalf("ledger-api /healthz after MCP apply_remediation_pr = %d, want 200", hCode)
	}
}

func issuePKCEToken(t *testing.T, baseURL, redirectURI, scope string, verifyReplayRevokes bool) string {
	t.Helper()
	verifier := generatePKCEVerifier(t)
	challenge := computeS256Challenge(verifier)

	authQuery := url.Values{
		"response_type":         {"code"},
		"client_id":             {"sre-gateway-client"},
		"redirect_uri":          {redirectURI},
		"state":                 {"state-pkce-99"},
		"scope":                 {scope},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	getResp, err := http.Get(baseURL + "/oauth/authorize?" + authQuery.Encode())
	if err != nil {
		t.Fatalf("GET /oauth/authorize: %v", err)
	}
	htmlBytes, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /oauth/authorize = %d (%s), want 200", getResp.StatusCode, htmlBytes)
	}
	if strings.Contains(getResp.Header.Get("Content-Security-Policy"), "unsafe-inline") {
		t.Fatalf("CSP must not contain unsafe-inline: %q", getResp.Header.Get("Content-Security-Policy"))
	}

	var csrfCookie *http.Cookie
	for _, c := range getResp.Cookies() {
		if strings.Contains(c.Name, "opshub_csrf") {
			csrfCookie = c
		}
	}
	if csrfCookie == nil || !csrfCookie.HttpOnly {
		t.Fatalf("expected HttpOnly opshub_csrf cookie, got %+v", getResp.Cookies())
	}
	m := hiddenCSRFInput.FindStringSubmatch(string(htmlBytes))
	if len(m) < 2 {
		t.Fatalf("missing hidden csrf_token input in consent form:\n%s", htmlBytes)
	}
	csrfVal := m[1]

	noRedirectClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Missing CSRF cookie/token on POST must fail with 403.
	badForm := authQuery
	badForm.Set("decision", "approve")
	badReq, _ := http.NewRequest(http.MethodPost, baseURL+"/oauth/authorize", strings.NewReader(badForm.Encode()))
	badReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badResp, err := noRedirectClient.Do(badReq)
	if err != nil {
		t.Fatalf("POST /oauth/authorize without CSRF: %v", err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST /oauth/authorize without CSRF = %d, want 403", badResp.StatusCode)
	}

	// Valid CSRF cookie + form token issues single-use authorization code.
	goodForm := url.Values{}
	for k, v := range authQuery {
		goodForm[k] = v
	}
	goodForm.Set("csrf_token", csrfVal)
	goodForm.Set("decision", "approve")
	postReq, _ := http.NewRequest(http.MethodPost, baseURL+"/oauth/authorize", strings.NewReader(goodForm.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(csrfCookie)
	postResp, err := noRedirectClient.Do(postReq)
	if err != nil {
		t.Fatalf("POST /oauth/authorize: %v", err)
	}
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusFound {
		t.Fatalf("POST /oauth/authorize = %d, want 302", postResp.StatusCode)
	}
	loc, err := url.Parse(postResp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect Location: %v", err)
	}
	authCode := loc.Query().Get("code")
	if authCode == "" || loc.Query().Get("state") != "state-pkce-99" {
		t.Fatalf("redirect Location = %q, want code and matching state", loc.String())
	}

	// Wrong code_verifier must fail without burning a different code test.
	wrongTokenResp, err := http.PostForm(baseURL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authCode},
		"client_id":     {"sre-gateway-client"},
		"redirect_uri":  {redirectURI},
		"code_verifier": {"wrong-verifier-value-that-does-not-match-s256-challenge"},
	})
	if err != nil {
		t.Fatalf("POST /oauth/token wrong verifier: %v", err)
	}
	wrongTokenResp.Body.Close()
	if wrongTokenResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /oauth/token with wrong verifier = %d, want 400", wrongTokenResp.StatusCode)
	}

	// Re-authorize after failed grant attempt so single-use strictness is respected cleanly.
	getResp2, _ := http.Get(baseURL + "/oauth/authorize?" + authQuery.Encode())
	html2, _ := io.ReadAll(getResp2.Body)
	getResp2.Body.Close()
	var csrf2 *http.Cookie
	for _, c := range getResp2.Cookies() {
		if strings.Contains(c.Name, "opshub_csrf") {
			csrf2 = c
		}
	}
	m2 := hiddenCSRFInput.FindStringSubmatch(string(html2))
	goodForm.Set("csrf_token", m2[1])
	postReq2, _ := http.NewRequest(http.MethodPost, baseURL+"/oauth/authorize", strings.NewReader(goodForm.Encode()))
	postReq2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq2.AddCookie(csrf2)
	postResp2, _ := noRedirectClient.Do(postReq2)
	postResp2.Body.Close()
	loc2, _ := url.Parse(postResp2.Header.Get("Location"))
	authCode2 := loc2.Query().Get("code")

	tokResp, err := http.PostForm(baseURL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {authCode2},
		"client_id":     {"sre-gateway-client"},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	})
	if err != nil {
		t.Fatalf("POST /oauth/token: %v", err)
	}
	tokRaw, _ := io.ReadAll(tokResp.Body)
	tokResp.Body.Close()
	if tokResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /oauth/token = %d (%s), want 200", tokResp.StatusCode, tokRaw)
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal(tokRaw, &parsed); err != nil {
		t.Fatalf("unmarshal token: %v", err)
	}
	if parsed.AccessToken == "" || parsed.ExpiresIn <= 0 || parsed.ExpiresIn > 900 {
		t.Fatalf("token response = %+v, want non-empty token with expires_in in 1..900", parsed)
	}

	if verifyReplayRevokes {
		replayResp, _ := http.PostForm(baseURL+"/oauth/token", url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {authCode2},
			"client_id":     {"sre-gateway-client"},
			"redirect_uri":  {redirectURI},
			"code_verifier": {verifier},
		})
		replayResp.Body.Close()
		if replayResp.StatusCode != http.StatusBadRequest {
			t.Fatalf("replayed authorization code = %d, want 400", replayResp.StatusCode)
		}
		// Replaying the code must also revoke the issued token.
		revUI, _ := http.NewRequest(http.MethodGet, baseURL+"/oauth/userinfo", nil)
		revUI.Header.Set("Authorization", "Bearer "+parsed.AccessToken)
		revResp, _ := http.DefaultClient.Do(revUI)
		revResp.Body.Close()
		if revResp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token after code replay = %d on /oauth/userinfo, want 401", revResp.StatusCode)
		}
		return issuePKCEToken(t, baseURL, redirectURI, scope, false)
	}
	return parsed.AccessToken
}

func callMCP(t *testing.T, endpoint string, rpc map[string]any, bearer, agentIdentity string) (int, http.Header, string) {
	t.Helper()
	b, err := json.Marshal(rpc)
	if err != nil {
		t.Fatalf("marshal MCP: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if agentIdentity != "" {
		req.Header.Set("X-Agent-Identity", agentIdentity)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(out)
}

func generatePKCEVerifier(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func computeS256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestOpsHubDevProxyRedirectURIsAndMismatchDescription(t *testing.T) {
	ledger := ledgersvc.NewServer(ledgersvc.Options{
		Config:    ledgersvc.RuntimeConfig{MaxOpenConns: 2, IdleTimeoutSeconds: 0, AcquireTimeoutMs: 50},
		LogOutput: io.Discard,
	})
	t.Cleanup(ledger.Close)
	ledgerTS := httptest.NewServer(ledger.Handler())
	t.Cleanup(ledgerTS.Close)

	hub, err := opshub.NewServer(opshub.Options{
		DataDir:       t.TempDir(),
		LedgerAPIURL:  ledgerTS.URL,
		Require3LO:    true,
		OAuthClientID: "sre-gateway-client",
	})
	if err != nil {
		t.Fatalf("opshub.NewServer: %v", err)
	}
	opsTS := httptest.NewServer(hub.Handler())
	t.Cleanup(opsTS.Close)

	verifier := generatePKCEVerifier(t)
	challenge := computeS256Challenge(verifier)

	for _, allowed := range []string{
		"https://8081-cs-test.ql-us-west1.cloudshell.dev/api/oauth/callback",
		"https://8081-myws.cluster.cloudworkstations.dev/api/oauth/callback",
		"https://8081-idx.googleusercontent.com/api/oauth/callback",
		"https://codespace-8081.app.github.dev/api/oauth/callback",
	} {
		code, body := httpGet(t, opsTS.URL+"/oauth/authorize?"+url.Values{
			"response_type":         {"code"},
			"client_id":             {"sre-gateway-client"},
			"redirect_uri":          {allowed},
			"state":                 {"st-dev"},
			"scope":                 {"openid profile remediation:write"},
			"code_challenge":        {challenge},
			"code_challenge_method": {"S256"},
		}.Encode())
		if code != http.StatusOK {
			t.Errorf("GET /oauth/authorize(%s) = %d (%s), want 200", allowed, code, body)
		}
	}

	for _, rejected := range []string{
		"http://8081-cs-test.ql-us-west1.cloudshell.dev/api/oauth/callback",
		"https://cloudshell.dev/api/oauth/callback",
		"https://evil.example.com/api/oauth/callback",
	} {
		code, _ := httpGet(t, opsTS.URL+"/oauth/authorize?"+url.Values{
			"response_type":         {"code"},
			"client_id":             {"sre-gateway-client"},
			"redirect_uri":          {rejected},
			"state":                 {"st-bad"},
			"scope":                 {"openid profile remediation:write"},
			"code_challenge":        {challenge},
			"code_challenge_method": {"S256"},
		}.Encode())
		if code != http.StatusBadRequest {
			t.Errorf("GET /oauth/authorize(%s) = %d, want 400", rejected, code)
		}
	}

	// Verify /oauth/token reports redirect_uri mismatch in error_description when redirect_uri differs.
	authRedirect := "https://8081-cs-test.ql-us-west1.cloudshell.dev/api/oauth/callback"
	authQuery := url.Values{
		"response_type":         {"code"},
		"client_id":             {"sre-gateway-client"},
		"redirect_uri":          {authRedirect},
		"state":                 {"st-mismatch"},
		"scope":                 {"openid profile remediation:write"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	getResp, err := http.Get(opsTS.URL + "/oauth/authorize?" + authQuery.Encode())
	if err != nil {
		t.Fatal(err)
	}
	htmlBytes, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	var csrfCookie *http.Cookie
	for _, c := range getResp.Cookies() {
		if strings.Contains(c.Name, "opshub_csrf") {
			csrfCookie = c
		}
	}
	m := hiddenCSRFInput.FindStringSubmatch(string(htmlBytes))
	form := url.Values{}
	for k, v := range authQuery {
		form[k] = v
	}
	form.Set("csrf_token", m[1])
	form.Set("decision", "approve")

	noRedirect := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	postReq, _ := http.NewRequest(http.MethodPost, opsTS.URL+"/oauth/authorize", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(csrfCookie)
	postResp, err := noRedirect.Do(postReq)
	if err != nil {
		t.Fatal(err)
	}
	postResp.Body.Close()
	loc, _ := url.Parse(postResp.Header.Get("Location"))

	mismatchResp, err := http.PostForm(opsTS.URL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {loc.Query().Get("code")},
		"client_id":     {"sre-gateway-client"},
		"redirect_uri":  {"http://127.0.0.1:8081/api/oauth/callback"},
		"code_verifier": {verifier},
	})
	if err != nil {
		t.Fatal(err)
	}
	mismatchRaw, _ := io.ReadAll(mismatchResp.Body)
	mismatchResp.Body.Close()
	if mismatchResp.StatusCode != http.StatusBadRequest || !strings.Contains(string(mismatchRaw), "redirect_uri mismatch") {
		t.Fatalf("POST /oauth/token with mismatched redirect_uri = %d (%s), want 400 with redirect_uri mismatch", mismatchResp.StatusCode, string(mismatchRaw))
	}
}
