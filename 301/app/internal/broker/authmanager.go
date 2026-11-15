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

package broker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/httptransport"
	"google.golang.org/adk/v2/auth"
	"google.golang.org/adk/v2/auth/gcp"
)

const (
	default3LOGrantTTL = 15 * time.Minute
	max3LOGrantTTL     = 15 * time.Minute
	pendingPKCETTL     = 5 * time.Minute
	default3LOOperator = "sre-operator"
)

var defaultRemediationScopes = []string{"openid", "profile", "remediation:write"}

// OAuthStatus reports non-sensitive 3LO grant metadata on GET /api/governance.
// Raw access tokens, refresh tokens, and PKCE verifiers are never included.
type OAuthStatus struct {
	RequiredForRemediation bool     `json:"required_for_remediation"`
	Provider               string   `json:"provider"`
	AuthProviderID         string   `json:"auth_provider_id,omitempty"`
	Granted                bool     `json:"granted"`
	OperatorSub            string   `json:"operator_sub,omitempty"`
	Scopes                 []string `json:"scopes,omitempty"`
	AuthMethod             string   `json:"auth_method,omitempty"`
	ExpiresAt              string   `json:"expires_at,omitempty"`
	TTLRemainingSeconds    int      `json:"ttl_remaining_seconds"`
}

// ConsentChallenge carries the authorization URL when interactive 3LO consent is needed.
type ConsentChallenge struct {
	ConsentRequired bool     `json:"consent_required"`
	AuthURL         string   `json:"auth_url"`
	State           string   `json:"state,omitempty"`
	Provider        string   `json:"provider"`
	Scopes          []string `json:"scopes"`
}

type pendingPKCEChallenge struct {
	state        string
	userID       string
	provider     string
	scopes       []string
	redirectURI  string
	codeVerifier string
	ttlOverride  time.Duration
	expiresAt    time.Time
}

type pendingGCPConsent struct {
	nonce       string
	userID      string
	continueURI string
	scopes      []string
	ttlOverride time.Duration
	expiresAt   time.Time
}

type activeGrantMeta struct {
	userID      string
	operatorSub string
	provider    string
	authMethod  string
	scopes      []string
	issuedAt    time.Time
	expiresAt   time.Time
}

type authManager struct {
	require3LO          bool
	opsHubURL           string
	oauthClientID       string
	oauthClientSecret   string
	githubClientID      string
	githubClientSecret  string
	gcpAuthProviderID   string
	authIdentityBaseURL string
	httpClient          *http.Client
	gcpHTTPClient       *http.Client
	gcpClient           *gcp.Client
	store               auth.CredentialStore
	recordPolicy        func(PolicyEvent)

	mu                 sync.Mutex
	grants             map[string]activeGrantMeta
	pendingPKCE        map[string]pendingPKCEChallenge
	consumedStates     map[string]time.Time
	pendingGCPConsents map[string]pendingGCPConsent
}

func newAuthManager(ctx context.Context, cfg Config, record func(PolicyEvent)) (*authManager, error) {
	providerRes := normalizeOptionalID(cfg.GCPAuthProviderID)
	authBase := strings.TrimRight(strings.TrimSpace(cfg.AuthIdentityUpstreamURL), "/")
	clientID := firstNonEmpty(cfg.OAuthClientID, "sre-gateway-3lo")

	noRedirectHC := &http.Client{
		Timeout: 12 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	m := &authManager{
		require3LO:          cfg.Require3LO,
		opsHubURL:           strings.TrimRight(cfg.OpsHubURL, "/"),
		oauthClientID:       clientID,
		oauthClientSecret:   strings.TrimSpace(cfg.OAuthClientSecret),
		githubClientID:      strings.TrimSpace(cfg.GitHubOAuthClientID),
		githubClientSecret:  strings.TrimSpace(cfg.GitHubOAuthClientSecret),
		gcpAuthProviderID:   providerRes,
		authIdentityBaseURL: authBase,
		httpClient:          &http.Client{Timeout: 12 * time.Second},
		store:               auth.NewInMemoryCredentialStore(),
		recordPolicy:        record,
		grants:              make(map[string]activeGrantMeta),
		pendingPKCE:         make(map[string]pendingPKCEChallenge),
		consumedStates:      make(map[string]time.Time),
		pendingGCPConsents:  make(map[string]pendingGCPConsent),
	}

	if providerRes != "" {
		gcpCfg := &gcp.Config{
			PollTimeout: 4 * time.Second,
		}
		if authBase != "" {
			gcpCfg.AgentIdentityEndpoint = authBase
			gcpCfg.ConnectorEndpoint = authBase
			gcpCfg.HTTPClient = noRedirectHC
			m.gcpHTTPClient = noRedirectHC
		} else if !cfg.MockLLM && strings.TrimSpace(cfg.ProjectID) != "" {
			if creds, err := credentials.DetectDefault(&credentials.DetectOptions{
				Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
			}); err == nil {
				if hc, err := httptransport.NewClient(&httptransport.Options{
					Credentials: creds,
				}); err == nil {
					hc.Timeout = 12 * time.Second
					hc.CheckRedirect = func(*http.Request, []*http.Request) error {
						return http.ErrUseLastResponse
					}
					gcpCfg.HTTPClient = hc
					m.gcpHTTPClient = hc
				}
			}
		}
		if gcpCfg.HTTPClient != nil {
			if cli, err := gcp.NewClient(ctx, gcpCfg); err == nil {
				m.gcpClient = cli
			}
		}
	}
	return m, nil
}

func (m *authManager) providerLabel() string {
	switch {
	case m.gcpAuthProviderID != "" && m.gcpClient != nil:
		return "agent-identity-auth-manager"
	case m.githubClientID != "":
		return "github-oauth-3lo"
	default:
		return "ops-hub-3lo"
	}
}

func (m *authManager) credKey(userID string) auth.CredentialKey {
	return auth.CredentialKey{
		AppName: "sre-gateway-3lo",
		UserID:  firstNonEmpty(userID, default3LOOperator),
		Key:     "remediation:write",
	}
}

func (m *authManager) Status(userID string) OAuthStatus {
	uid := firstNonEmpty(userID, default3LOOperator)
	st := OAuthStatus{
		RequiredForRemediation: m.require3LO,
		Provider:               m.providerLabel(),
		AuthProviderID:         m.gcpAuthProviderID,
		Scopes:                 append([]string(nil), defaultRemediationScopes...),
	}
	tok, meta, ok := m.ActiveToken(context.Background(), uid)
	if !ok || tok == "" {
		return st
	}
	rem := int(math.Ceil(time.Until(meta.expiresAt).Seconds()))
	if rem < 1 {
		rem = 1
	}
	st.Granted = true
	st.OperatorSub = meta.operatorSub
	st.AuthMethod = meta.authMethod
	st.Scopes = append([]string(nil), meta.scopes...)
	st.ExpiresAt = meta.expiresAt.UTC().Format(time.RFC3339)
	st.TTLRemainingSeconds = rem
	return st
}

// ActiveToken returns the active short-lived 3LO token from the in-memory vault,
// pruning expired entries with exact wall-clock precision.
func (m *authManager) ActiveToken(ctx context.Context, userID string) (string, activeGrantMeta, bool) {
	uid := firstNonEmpty(userID, default3LOOperator)
	key := m.credKey(uid)
	now := time.Now().UTC()

	m.mu.Lock()
	meta, hasMeta := m.grants[uid]
	if !hasMeta || !now.Before(meta.expiresAt) {
		if hasMeta {
			delete(m.grants, uid)
		}
		m.mu.Unlock()
		_ = m.store.Delete(ctx, key)
		return "", activeGrantMeta{}, false
	}
	m.mu.Unlock()

	cred, hit, err := m.store.Get(ctx, key)
	if err != nil || !hit || cred == nil {
		m.mu.Lock()
		delete(m.grants, uid)
		m.mu.Unlock()
		return "", activeGrantMeta{}, false
	}
	tok := extractBearerFromCredential(cred)
	if tok == "" {
		return "", activeGrantMeta{}, false
	}
	return tok, meta, true
}

func (m *authManager) storeGrant(ctx context.Context, userID, operatorSub, provider, authMethod, token string, scopes []string, ttl time.Duration) activeGrantMeta {
	uid := firstNonEmpty(userID, default3LOOperator)
	if ttl <= 0 || ttl > max3LOGrantTTL {
		ttl = default3LOGrantTTL
	}
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)

	meta := activeGrantMeta{
		userID:      uid,
		operatorSub: firstNonEmpty(operatorSub, uid),
		provider:    provider,
		authMethod:  authMethod,
		scopes:      append([]string(nil), scopes...),
		issuedAt:    now,
		expiresAt:   expiresAt,
	}

	m.mu.Lock()
	m.grants[uid] = meta
	m.mu.Unlock()

	// Add auth.ExpirySkew when writing to InMemoryCredentialStore so short TTLs (< 10s)
	// remain readable until exact wall-clock expiresAt enforced in ActiveToken.
	_ = m.store.Set(ctx, m.credKey(uid), auth.BearerCredential{Token: token}, expiresAt.Add(auth.ExpirySkew))
	return meta
}

func (m *authManager) Revoke(ctx context.Context, userID string) {
	uid := firstNonEmpty(userID, default3LOOperator)
	m.mu.Lock()
	delete(m.grants, uid)
	m.mu.Unlock()
	_ = m.store.Delete(ctx, m.credKey(uid))
	if m.recordPolicy != nil {
		m.recordPolicy(PolicyEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Boundary:  "Auth Manager (3LO)",
			Action:    "Revoke3LOGrant (" + uid + ")",
			Verdict:   "REVOKED",
			Detail:    "Purged short-lived 3LO grant from sre-gateway server-side in-memory vault",
		})
	}
}

// ResolveForRemediation resolves an active 3LO token or initiates a consent challenge.
func (m *authManager) ResolveForRemediation(ctx context.Context, baseURL, userID string) (string, ConsentChallenge, error) {
	uid := firstNonEmpty(userID, default3LOOperator)
	if tok, _, ok := m.ActiveToken(ctx, uid); ok {
		return tok, ConsentChallenge{}, nil
	}
	return m.StartOrRetrieve(ctx, baseURL, uid, 0)
}

func (m *authManager) StartOrRetrieve(ctx context.Context, baseURL, userID string, ttlOverride time.Duration) (string, ConsentChallenge, error) {
	uid := firstNonEmpty(userID, default3LOOperator)
	scopes := append([]string(nil), defaultRemediationScopes...)
	if m.githubClientID != "" && m.gcpAuthProviderID == "" {
		scopes = []string{"repo"}
	}
	continueURI := strings.TrimRight(baseURL, "/") + "/api/oauth/callback"

	if m.gcpClient != nil && m.gcpAuthProviderID != "" {
		ret, err := m.gcpClient.RetrieveCredential(ctx, gcp.Request{
			Resource:    m.gcpAuthProviderID,
			UserID:      uid,
			Scopes:      scopes,
			ContinueURI: continueURI,
		})
		if err == nil && ret != nil && ret.Credential != nil {
			tok := extractBearerFromCredential(ret.Credential)
			if tok != "" {
				ttl := default3LOGrantTTL
				if !ret.ExpiresAt.IsZero() {
					if rem := time.Until(ret.ExpiresAt); rem > 0 && rem < ttl {
						ttl = rem
					}
				}
				if ttlOverride > 0 && ttlOverride < ttl {
					ttl = ttlOverride
				}
				m.storeGrant(ctx, uid, uid, "agent-identity-auth-manager", "agent-identity-auth-manager", tok, scopes, ttl)
				return tok, ConsentChallenge{}, nil
			}
		}
		var consentErr *auth.ConsentRequiredError
		if errors.As(err, &consentErr) && consentErr != nil && consentErr.AuthURI != "" {
			now := time.Now().UTC()
			nonce := firstNonEmpty(consentErr.Nonce, mustRandHex(16))
			m.mu.Lock()
			m.pruneLocked(now)
			m.pendingGCPConsents[nonce] = pendingGCPConsent{
				nonce:       nonce,
				userID:      uid,
				continueURI: continueURI,
				scopes:      scopes,
				ttlOverride: ttlOverride,
				expiresAt:   now.Add(pendingPKCETTL),
			}
			m.mu.Unlock()
			return "", ConsentChallenge{
				ConsentRequired: true,
				AuthURL:         consentErr.AuthURI,
				State:           nonce,
				Provider:        "agent-identity-auth-manager",
				Scopes:          scopes,
			}, nil
		}
		// If AuthIdentityUpstreamURL was explicitly pinned (test seam), return the error directly.
		if m.authIdentityBaseURL != "" && err != nil {
			return "", ConsentChallenge{}, err
		}
	}

	ch, err := m.issuePKCEChallenge(baseURL, uid, scopes, ttlOverride)
	if err != nil {
		return "", ConsentChallenge{}, err
	}
	return "", ch, nil
}

func (m *authManager) issuePKCEChallenge(baseURL, userID string, scopes []string, ttlOverride time.Duration) (ConsentChallenge, error) {
	verifier, err := generateS256Verifier()
	if err != nil {
		return ConsentChallenge{}, err
	}
	challenge := computeS256FromVerifier(verifier)
	stateNonce, err := randomHex(24)
	if err != nil {
		return ConsentChallenge{}, err
	}

	redirectURI := strings.TrimRight(baseURL, "/") + "/api/oauth/callback"
	provider := "ops-hub-3lo"
	clientID := m.oauthClientID
	authEndpoint := strings.TrimRight(baseURL, "/") + "/oauth/authorize"
	if m.githubClientID != "" {
		provider = "github-oauth-3lo"
		clientID = m.githubClientID
		authEndpoint = "https://github.com/login/oauth/authorize"
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", stateNonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")

	now := time.Now().UTC()
	m.mu.Lock()
	m.pruneLocked(now)
	m.pendingPKCE[stateNonce] = pendingPKCEChallenge{
		state:        stateNonce,
		userID:       userID,
		provider:     provider,
		scopes:       scopes,
		redirectURI:  redirectURI,
		codeVerifier: verifier,
		ttlOverride:  ttlOverride,
		expiresAt:    now.Add(pendingPKCETTL),
	}
	m.mu.Unlock()

	return ConsentChallenge{
		ConsentRequired: true,
		AuthURL:         authEndpoint + "?" + q.Encode(),
		State:           stateNonce,
		Provider:        provider,
		Scopes:          scopes,
	}, nil
}

func (m *authManager) finalizeGCPConsent(ctx context.Context, consentNonce string) (activeGrantMeta, error) {
	now := time.Now().UTC()
	m.mu.Lock()
	m.pruneLocked(now)
	pend, ok := m.pendingGCPConsents[consentNonce]
	if ok {
		delete(m.pendingGCPConsents, consentNonce)
	}
	m.mu.Unlock()
	if !ok {
		return activeGrantMeta{}, errors.New("unknown or expired Auth Manager consent_nonce")
	}

	base := m.authIdentityBaseURL
	apiVer := "v1"
	if strings.Contains(m.gcpAuthProviderID, "/connectors/") {
		apiVer = "v1alpha"
		if base == "" {
			base = "https://iamconnectorcredentials.googleapis.com"
		}
	} else if base == "" {
		base = "https://agentidentitycredentials.googleapis.com"
	}

	finURL := fmt.Sprintf("%s/%s/%s/credentials:finalize", base, apiVer, m.gcpAuthProviderID)
	finPayload, _ := json.Marshal(map[string]any{
		"userId":                pend.userID,
		"userIdValidationState": "USER_ID_VALIDATED",
		"consentNonce":          consentNonce,
	})
	finReq, err := http.NewRequestWithContext(ctx, http.MethodPost, finURL, bytes.NewReader(finPayload))
	if err != nil {
		return activeGrantMeta{}, err
	}
	finReq.Header.Set("Content-Type", "application/json")
	hc := m.gcpHTTPClient
	if hc == nil {
		hc = m.httpClient
	}
	finResp, err := hc.Do(finReq)
	if err != nil {
		return activeGrantMeta{}, fmt.Errorf("finalize Auth Manager consent: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(finResp.Body, 64<<10))
	_ = finResp.Body.Close()
	if finResp.StatusCode < 200 || finResp.StatusCode >= 300 {
		return activeGrantMeta{}, fmt.Errorf("finalize Auth Manager consent returned HTTP %d", finResp.StatusCode)
	}

	ret, err := m.gcpClient.RetrieveCredential(ctx, gcp.Request{
		Resource:    m.gcpAuthProviderID,
		UserID:      pend.userID,
		Scopes:      pend.scopes,
		ContinueURI: pend.continueURI,
	})
	if err != nil {
		return activeGrantMeta{}, fmt.Errorf("retrieve credential after finalize: %w", err)
	}
	tok := extractBearerFromCredential(ret.Credential)
	if tok == "" {
		return activeGrantMeta{}, errors.New("Auth Manager returned an empty bearer credential")
	}
	ttl := default3LOGrantTTL
	if !ret.ExpiresAt.IsZero() {
		if rem := time.Until(ret.ExpiresAt); rem > 0 && rem < ttl {
			ttl = rem
		}
	}
	if pend.ttlOverride > 0 && pend.ttlOverride < ttl {
		ttl = pend.ttlOverride
	}

	meta := m.storeGrant(ctx, pend.userID, pend.userID, "agent-identity-auth-manager", "agent-identity-auth-manager", tok, pend.scopes, ttl)
	if m.recordPolicy != nil {
		m.recordPolicy(PolicyEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Boundary:  "Auth Manager (3LO)",
			Action:    "FinalizeCredential (" + m.gcpAuthProviderID + ")",
			Verdict:   "GRANTED",
			Detail:    fmt.Sprintf("Minted short-lived 3LO grant via adk/v2/auth/gcp for %s (ttl=%ds, server-side memory only)", meta.operatorSub, int(ttl.Seconds())),
		})
	}
	return meta, nil
}

func (m *authManager) exchangePKCECallback(ctx context.Context, code, stateNonce string) (activeGrantMeta, error) {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(stateNonce) == "" {
		return activeGrantMeta{}, errors.New("missing OAuth code or state parameter")
	}
	now := time.Now().UTC()
	m.mu.Lock()
	m.pruneLocked(now)
	if exp, used := m.consumedStates[stateNonce]; used && now.Before(exp) {
		m.mu.Unlock()
		return activeGrantMeta{}, errors.New("OAuth state parameter already consumed")
	}
	pend, ok := m.pendingPKCE[stateNonce]
	if ok {
		delete(m.pendingPKCE, stateNonce)
		m.consumedStates[stateNonce] = now.Add(default3LOGrantTTL)
	}
	m.mu.Unlock()
	if !ok {
		return activeGrantMeta{}, errors.New("unknown or expired OAuth state parameter")
	}

	tokenEndpoint := m.opsHubURL + "/oauth/token"
	clientID := m.oauthClientID
	clientSecret := m.oauthClientSecret
	if pend.provider == "github-oauth-3lo" {
		tokenEndpoint = "https://github.com/login/oauth/access_token"
		clientID = m.githubClientID
		clientSecret = m.githubClientSecret
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("redirect_uri", pend.redirectURI)
	form.Set("code_verifier", pend.codeVerifier)
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return activeGrantMeta{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return activeGrantMeta{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return activeGrantMeta{}, fmt.Errorf("token endpoint returned HTTP %d", resp.StatusCode)
	}

	var tokResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &tokResp); err != nil || strings.TrimSpace(tokResp.AccessToken) == "" {
		return activeGrantMeta{}, errors.New("token endpoint did not return a valid access_token")
	}

	operatorSub := pend.userID
	if pend.provider == "ops-hub-3lo" {
		if sub := m.fetchOpsHubOperatorSub(ctx, tokResp.AccessToken); sub != "" {
			operatorSub = sub
		}
	}

	ttl := default3LOGrantTTL
	if tokResp.ExpiresIn > 0 {
		if d := time.Duration(tokResp.ExpiresIn) * time.Second; d < ttl {
			ttl = d
		}
	}
	if pend.ttlOverride > 0 && pend.ttlOverride < ttl {
		ttl = pend.ttlOverride
	}

	scopes := pend.scopes
	if strings.TrimSpace(tokResp.Scope) != "" {
		scopes = strings.Fields(tokResp.Scope)
	}
	meta := m.storeGrant(ctx, pend.userID, operatorSub, pend.provider, "oauth2_pkce_s256", strings.TrimSpace(tokResp.AccessToken), scopes, ttl)
	if m.recordPolicy != nil {
		m.recordPolicy(PolicyEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Boundary:  "Auth Manager (3LO)",
			Action:    "OAuthCallback (" + pend.provider + ")",
			Verdict:   "GRANTED",
			Detail:    fmt.Sprintf("Exchanged S256 PKCE code for short-lived 3LO grant (operator=%s, ttl=%ds, server-side memory only)", meta.operatorSub, int(ttl.Seconds())),
		})
	}
	return meta, nil
}

func (m *authManager) fetchOpsHubOperatorSub(ctx context.Context, accessToken string) string {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, m.opsHubURL+"/oauth/userinfo", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var ui struct {
		Sub string `json:"sub"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&ui)
	return strings.TrimSpace(ui.Sub)
}

func (m *authManager) pruneLocked(now time.Time) {
	if len(m.pendingPKCE) > 512 {
		clear(m.pendingPKCE)
	}
	for k, v := range m.pendingPKCE {
		if !now.Before(v.expiresAt) {
			delete(m.pendingPKCE, k)
		}
	}
	for k, v := range m.consumedStates {
		if !now.Before(v) {
			delete(m.consumedStates, k)
		}
	}
	for k, v := range m.pendingGCPConsents {
		if !now.Before(v.expiresAt) {
			delete(m.pendingGCPConsents, k)
		}
	}
}

func extractBearerFromCredential(cred auth.Credential) string {
	if cred == nil {
		return ""
	}
	h := http.Header{}
	if err := cred.Apply(h); err != nil {
		return ""
	}
	raw := strings.TrimSpace(h.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(raw), "bearer ") {
		return strings.TrimSpace(raw[len("Bearer "):])
	}
	return ""
}

func generateS256Verifier() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func computeS256FromVerifier(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func mustRandHex(nBytes int) string {
	s, err := randomHex(nBytes)
	if err != nil {
		return "noncefallback01"
	}
	return s
}

// HTTP handlers on Server for 3LO OAuth start, callback, revoke, and ops-hub consent proxy.

func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var body struct {
		TTLSeconds int `json:"ttl_seconds"`
	}
	if r.Body != nil {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
		if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid OAuth start payload"})
				return
			}
		}
	}
	var ttlOverride time.Duration
	if body.TTLSeconds > 0 {
		ttlOverride = time.Duration(body.TTLSeconds) * time.Second
	}

	tok, ch, err := s.authManager.StartOrRetrieve(r.Context(), s.resolveBaseURL(r), default3LOOperator, ttlOverride)
	if err != nil {
		writeBrokerJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if tok != "" {
		st := s.authManager.Status(default3LOOperator)
		writeBrokerJSON(w, http.StatusOK, map[string]any{
			"consent_required":      false,
			"granted":               true,
			"provider":              st.Provider,
			"operator_sub":          st.OperatorSub,
			"ttl_remaining_seconds": st.TTLRemainingSeconds,
		})
		return
	}
	writeBrokerJSON(w, http.StatusOK, ch)
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	q := r.URL.Query()
	if oauthErr := strings.TrimSpace(q.Get("error")); oauthErr != "" {
		s.recordPolicyEvent(PolicyEvent{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Boundary:  "Auth Manager (3LO)",
			Action:    "OAuthCallback",
			Verdict:   "DENY",
			Detail:    "Operator denied 3LO OAuth consent (" + oauthErr + ")",
		})
		if wantsJSONCallback(r) {
			writeBrokerJSON(w, http.StatusForbidden, map[string]any{"granted": false, "error": oauthErr})
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	var (
		meta activeGrantMeta
		err  error
	)
	consentNonce := firstNonEmpty(q.Get("consent_nonce"), q.Get("consentNonce"))
	if consentNonce != "" {
		meta, err = s.authManager.finalizeGCPConsent(r.Context(), consentNonce)
	} else {
		meta, err = s.authManager.exchangePKCECallback(r.Context(), q.Get("code"), q.Get("state"))
	}
	if err != nil {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if wantsJSONCallback(r) {
		rem := int(math.Ceil(time.Until(meta.expiresAt).Seconds()))
		if rem < 1 {
			rem = 1
		}
		writeBrokerJSON(w, http.StatusOK, map[string]any{
			"granted":               true,
			"provider":              meta.provider,
			"auth_method":           meta.authMethod,
			"operator_sub":          meta.operatorSub,
			"expires_at":            meta.expiresAt.UTC().Format(time.RFC3339),
			"ttl_remaining_seconds": rem,
		})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleOAuthRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s.authManager.Revoke(r.Context(), default3LOOperator)
	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"granted": false,
		"revoked": true,
	})
}

// handleOpsHubConsentProxy proxies /oauth/authorize and /oauth/consent.css to ops-hub
// so browser 3LO consent works over Gateway/sre-ingress-gateway or localhost:8081 without
// exposing cluster-internal DNS names.
func (s *Server) handleOpsHubConsentProxy(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/oauth/authorize":
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, HEAD, POST")
			writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
	case "/oauth/consent.css":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
	default:
		http.NotFound(w, r)
		return
	}

	targetURL := s.cfg.OpsHubURL + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}
	var bodyReader io.Reader
	if r.Method == http.MethodPost && r.Body != nil {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			writeBrokerJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "consent form payload too large"})
			return
		}
		bodyReader = bytes.NewReader(raw)
	}

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bodyReader)
	if err != nil {
		writeBrokerJSON(w, http.StatusInternalServerError, map[string]string{"error": "construct consent proxy request failed"})
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		upReq.Header.Set("Content-Type", ct)
	}
	if isHTTPSRequest(r) {
		upReq.Header.Set("X-Forwarded-Proto", "https")
	}
	for _, c := range r.Cookies() {
		if strings.Contains(c.Name, "opshub_csrf") {
			upReq.AddCookie(c)
		}
	}

	noRedirect := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := noRedirect.Do(upReq)
	if err != nil {
		writeBrokerJSON(w, http.StatusBadGateway, map[string]string{"error": "ops-hub OAuth endpoint unreachable: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	for _, sc := range resp.Header.Values("Set-Cookie") {
		w.Header().Add("Set-Cookie", sc)
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		w.Header().Set("Location", loc)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 512<<10))
}

func wantsJSONCallback(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "json") {
		return true
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json")
}
