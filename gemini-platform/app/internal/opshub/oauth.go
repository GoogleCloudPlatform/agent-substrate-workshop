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
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	authCodeTTL    = 5 * time.Minute
	accessTokenTTL = 15 * time.Minute
)

var validPKCEChallenge = regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)

type authCodeRecord struct {
	code                string
	clientID            string
	redirectURI         string
	scope               string
	codeChallenge       string
	codeChallengeMethod string
	operatorSub         string
	expiresAt           time.Time
	used                bool
	issuedToken         string
}

type accessTokenRecord struct {
	token       string
	clientID    string
	scope       string
	operatorSub string
	expiresAt   time.Time
}

var consentPageTmpl = template.Must(template.New("consent").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Authorize remediation access · Operations Hub</title>
  <link rel="stylesheet" href="/oauth/consent.css">
</head>
<body>
  <main class="card">
    <h1>Authorize Operations Hub access</h1>
    <p class="subtitle">Client <code>{{.ClientID}}</code> requests delegated operator access for incident remediation.</p>
    <div class="meta">
      <div><span class="label">Operator</span> <code>{{.MaskedOperator}}</code></div>
      <div><span class="label">Requested scopes</span> <code>{{.Scope}}</code></div>
      <div><span class="label">Callback</span> <code>{{.RedirectURI}}</code></div>
    </div>
    <form method="post" action="/oauth/authorize">
      <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
      <input type="hidden" name="response_type" value="{{.ResponseType}}">
      <input type="hidden" name="client_id" value="{{.ClientID}}">
      <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
      <input type="hidden" name="state" value="{{.State}}">
      <input type="hidden" name="scope" value="{{.Scope}}">
      <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
      <input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}">
      <div class="actions">
        <button type="submit" name="decision" value="approve" class="btn-primary">Approve access</button>
        <button type="submit" name="decision" value="deny" class="btn-secondary">Deny</button>
      </div>
    </form>
  </main>
</body>
</html>
`))

const consentCSS = `:root {
  --cm-sys-color-surface: #ffffff;
  --cm-sys-color-neutral-container: #f1f3f4;
  --cm-sys-color-on-surface: #202124;
  --cm-sys-color-on-surface-variant: #5f6368;
  --cm-sys-color-primary: #1a73e8;
  --cm-sys-color-on-primary: #ffffff;
  --cm-sys-color-hairline: #dadce0;
  --cm-sys-color-outline: #80868b;
}
body {
  margin: 0;
  padding: 48px 16px;
  font-family: Roboto, system-ui, sans-serif;
  font-size: 13px;
  line-height: 20px;
  background: var(--cm-sys-color-neutral-container);
  color: var(--cm-sys-color-on-surface);
}
.card {
  max-width: 460px;
  margin: 0 auto;
  padding: 24px;
  background: var(--cm-sys-color-surface);
  border: 1px solid var(--cm-sys-color-hairline);
  border-radius: 4px;
}
h1 {
  margin: 0 0 8px;
  font-size: 20px;
  line-height: 28px;
  font-weight: 400;
}
.subtitle {
  margin: 0 0 16px;
  color: var(--cm-sys-color-on-surface-variant);
}
.meta {
  padding: 12px;
  margin-bottom: 20px;
  background: var(--cm-sys-color-neutral-container);
  border-radius: 4px;
}
.meta div + div { margin-top: 6px; }
.label {
  display: inline-block;
  min-width: 120px;
  font-weight: 500;
  color: var(--cm-sys-color-on-surface-variant);
}
code {
  font-family: "Roboto Mono", ui-monospace, monospace;
  font-size: 12px;
}
.actions {
  display: flex;
  gap: 8px;
}
button {
  height: 32px;
  padding: 0 12px;
  border-radius: 4px;
  font-family: inherit;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
}
.btn-primary {
  background: var(--cm-sys-color-primary);
  color: var(--cm-sys-color-on-primary);
  border: none;
}
.btn-secondary {
  background: transparent;
  color: var(--cm-sys-color-on-surface);
  border: 1px solid var(--cm-sys-color-outline);
}
`

func (s *Server) handleOAuthDiscovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	issuer := s.issuerURL(r)
	writeJSONResp(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/oauth/authorize",
		"token_endpoint":                        issuer + "/oauth/token",
		"userinfo_endpoint":                     issuer + "/oauth/userinfo",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                      []string{"openid", "profile", "incidents:read", "remediation:write"},
	})
}

func (s *Server) handleConsentCSS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(consentCSS))
}

func (s *Server) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.renderAuthorizeConsent(w, r)
	case http.MethodPost:
		s.submitAuthorizeConsent(w, r)
	default:
		writeJSONResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) renderAuthorizeConsent(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params, errMsg := s.validateAuthorizeParams(q)
	if errMsg != "" {
		writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": errMsg})
		return
	}

	csrfToken, err := randTokenHex(32)
	if err != nil {
		writeJSONResp(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	isHTTPS := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	// TODO(security): Classroom kubectl port-forward sessions run over plain HTTP on
	// localhost, where browsers reject __Host- cookies without Secure. HTTPS requests
	// always issue __Host-opshub_csrf with Secure=true and no Domain attribute.
	cookieName := "opshub_csrf"
	if isHTTPS {
		cookieName = "__Host-opshub_csrf"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    csrfToken,
		Path:     "/",
		MaxAge:   300,
		HttpOnly: true,
		Secure:   isHTTPS,
		SameSite: http.SameSiteLaxMode,
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = consentPageTmpl.Execute(w, map[string]string{
		"CSRFToken":           csrfToken,
		"ResponseType":        params.responseType,
		"ClientID":            params.clientID,
		"RedirectURI":         params.redirectURI,
		"State":               params.state,
		"Scope":               params.scope,
		"CodeChallenge":       params.codeChallenge,
		"CodeChallengeMethod": params.codeChallengeMethod,
		"MaskedOperator":      "s***@example.com",
	})
}

func (s *Server) submitAuthorizeConsent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyLen)
	if err := r.ParseForm(); err != nil {
		writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}

	formCSRF := strings.TrimSpace(r.PostFormValue("csrf_token"))
	cookieCSRF := readCSRFCookie(r)
	if formCSRF == "" || cookieCSRF == "" || subtle.ConstantTimeCompare([]byte(formCSRF), []byte(cookieCSRF)) != 1 {
		writeJSONResp(w, http.StatusForbidden, map[string]string{"error": "access_denied", "error_description": "CSRF validation failed"})
		return
	}

	params, errMsg := s.validateAuthorizeParams(r.PostForm)
	if errMsg != "" {
		writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": errMsg})
		return
	}

	cbURL, err := url.Parse(params.redirectURI)
	if err != nil {
		writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	cbQuery := cbURL.Query()
	if params.state != "" {
		cbQuery.Set("state", params.state)
	}
	if r.PostFormValue("decision") != "approve" {
		cbQuery.Set("error", "access_denied")
		cbURL.RawQuery = cbQuery.Encode()
		http.Redirect(w, r, cbURL.String(), http.StatusFound)
		return
	}

	code, err := randTokenHex(32)
	if err != nil {
		writeJSONResp(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	now := time.Now()
	s.mu.Lock()
	s.pruneExpiredOAuthLocked(now)
	s.authCodes[code] = &authCodeRecord{
		code:                code,
		clientID:            params.clientID,
		redirectURI:         params.redirectURI,
		scope:               params.scope,
		codeChallenge:       params.codeChallenge,
		codeChallengeMethod: params.codeChallengeMethod,
		operatorSub:         "sre-operator",
		expiresAt:           now.Add(authCodeTTL),
	}
	s.mu.Unlock()

	cbQuery.Set("code", code)
	cbURL.RawQuery = cbQuery.Encode()
	http.Redirect(w, r, cbURL.String(), http.StatusFound)
}

type authorizeParams struct {
	responseType        string
	clientID            string
	redirectURI         string
	state               string
	scope               string
	codeChallenge       string
	codeChallengeMethod string
}

func (s *Server) validateAuthorizeParams(vals url.Values) (authorizeParams, string) {
	p := authorizeParams{
		responseType:        strings.TrimSpace(vals.Get("response_type")),
		clientID:            strings.TrimSpace(vals.Get("client_id")),
		redirectURI:         strings.TrimSpace(vals.Get("redirect_uri")),
		state:               strings.TrimSpace(vals.Get("state")),
		scope:               strings.TrimSpace(vals.Get("scope")),
		codeChallenge:       strings.TrimSpace(vals.Get("code_challenge")),
		codeChallengeMethod: strings.TrimSpace(vals.Get("code_challenge_method")),
	}
	if p.responseType != "code" {
		return authorizeParams{}, "response_type must be code"
	}
	if p.clientID == "" || len(p.clientID) > 128 {
		return authorizeParams{}, "client_id is required"
	}
	if s.oauthClientID != "" && p.clientID != s.oauthClientID {
		return authorizeParams{}, "unrecognized client_id"
	}
	if !s.isAllowedRedirectURI(p.redirectURI) {
		return authorizeParams{}, "redirect_uri is not on the allowed callback list"
	}
	if p.scope == "" {
		p.scope = "openid profile remediation:write"
	}
	if p.codeChallengeMethod != "" && p.codeChallengeMethod != "S256" {
		return authorizeParams{}, "code_challenge_method must be S256"
	}
	if p.codeChallenge != "" {
		if p.codeChallengeMethod == "" {
			p.codeChallengeMethod = "S256"
		}
		if !validPKCEChallenge.MatchString(p.codeChallenge) {
			return authorizeParams{}, "code_challenge must be 43..128 base64url characters"
		}
	}
	return p, ""
}

func (s *Server) isAllowedRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch u.Scheme {
	case "http":
		if host == "127.0.0.1" || host == "localhost" {
			return true
		}
		if strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local") {
			return true
		}
		if s.publicBaseURL != "" {
			if pub, err := url.Parse(s.publicBaseURL); err == nil && strings.EqualFold(pub.Hostname(), host) {
				return true
			}
		}
		return false
	case "https":
		if host == "127.0.0.1" || host == "localhost" {
			return true
		}
		if strings.HasSuffix(host, ".googleapis.com") || strings.HasSuffix(host, ".cloudshell.dev") || strings.HasSuffix(host, ".svc.cluster.local") {
			return true
		}
		if s.publicBaseURL != "" {
			if pub, err := url.Parse(s.publicBaseURL); err == nil && strings.EqualFold(pub.Hostname(), host) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyLen)

	var grantType, code, clientID, clientSecret, redirectURI, verifier string
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		grantType = body["grant_type"]
		code = body["code"]
		clientID = body["client_id"]
		clientSecret = body["client_secret"]
		redirectURI = body["redirect_uri"]
		verifier = body["code_verifier"]
	} else {
		if err := r.ParseForm(); err != nil {
			writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		grantType = r.PostFormValue("grant_type")
		code = r.PostFormValue("code")
		clientID = r.PostFormValue("client_id")
		clientSecret = r.PostFormValue("client_secret")
		redirectURI = r.PostFormValue("redirect_uri")
		verifier = r.PostFormValue("code_verifier")
	}

	if basicID, basicSecret, ok := r.BasicAuth(); ok {
		if clientID == "" {
			clientID = basicID
		}
		if clientSecret == "" {
			clientSecret = basicSecret
		}
	}

	if grantType != "authorization_code" || strings.TrimSpace(code) == "" {
		writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredOAuthLocked(now)

	rec, ok := s.authCodes[code]
	if !ok {
		writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	if rec.used {
		// RFC 6749 Section 10.5: revoke tokens previously issued from a replayed code.
		if rec.issuedToken != "" {
			delete(s.accessTokens, rec.issuedToken)
		}
		delete(s.authCodes, code)
		writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "authorization code already used"})
		return
	}
	rec.used = true

	if now.After(rec.expiresAt) || (clientID != "" && clientID != rec.clientID) || redirectURI != rec.redirectURI {
		delete(s.authCodes, code)
		writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}

	if rec.codeChallenge != "" {
		if !verifyPKCES256(verifier, rec.codeChallenge) {
			delete(s.authCodes, code)
			writeJSONResp(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "PKCE code_verifier mismatch"})
			return
		}
	} else {
		if clientSecret == "" || subtle.ConstantTimeCompare([]byte(clientSecret), []byte(s.oauthClientSecret)) != 1 {
			delete(s.authCodes, code)
			writeJSONResp(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
			return
		}
	}

	rawTok, err := randTokenHex(32)
	if err != nil {
		writeJSONResp(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	accessTok := "opshub_at_" + rawTok
	rec.issuedToken = accessTok
	s.accessTokens[accessTok] = &accessTokenRecord{
		token:       accessTok,
		clientID:    rec.clientID,
		scope:       rec.scope,
		operatorSub: rec.operatorSub,
		expiresAt:   now.Add(accessTokenTTL),
	}

	writeJSONResp(w, http.StatusOK, map[string]any{
		"access_token": accessTok,
		"token_type":   "Bearer",
		"expires_in":   int(accessTokenTTL.Seconds()),
		"scope":        rec.scope,
	})
}

func (s *Server) handleOAuthUserInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONResp(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	tokRec, ok := s.authenticateBearer(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ops-hub"`)
		writeJSONResp(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	writeJSONResp(w, http.StatusOK, map[string]any{
		"sub":   tokRec.operatorSub,
		"name":  "Platform SRE Operator",
		"email": "s***@example.com",
		"role":  "platform-sre",
		"scope": tokRec.scope,
	})
}

func (s *Server) authenticateBearer(r *http.Request) (accessTokenRecord, bool) {
	hdr := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(hdr, "Bearer ") {
		return accessTokenRecord{}, false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(hdr, "Bearer "))
	if tok == "" {
		return accessTokenRecord{}, false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredOAuthLocked(now)
	rec, ok := s.accessTokens[tok]
	if !ok || now.After(rec.expiresAt) {
		return accessTokenRecord{}, false
	}
	return *rec, true
}

func hasOAuthScope(scopeList, target string) bool {
	for _, sc := range strings.Fields(scopeList) {
		if sc == target {
			return true
		}
	}
	return false
}

func (s *Server) pruneExpiredOAuthLocked(now time.Time) {
	for k, v := range s.authCodes {
		if now.After(v.expiresAt) {
			delete(s.authCodes, k)
		}
	}
	for k, v := range s.accessTokens {
		if now.After(v.expiresAt) {
			delete(s.accessTokens, k)
		}
	}
}

func (s *Server) issuerURL(r *http.Request) string {
	if s.publicBaseURL != "" {
		return s.publicBaseURL
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func verifyPKCES256(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

func readCSRFCookie(r *http.Request) string {
	isHTTPS := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	if isHTTPS {
		if c, err := r.Cookie("__Host-opshub_csrf"); err == nil && c.Value != "" {
			return c.Value
		}
		return ""
	}
	for _, name := range []string{"__Host-opshub_csrf", "opshub_csrf"} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func randTokenHex(numBytes int) (string, error) {
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
