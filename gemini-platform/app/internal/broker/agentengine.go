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
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/httptransport"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/agentruntime"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

const (
	maxAgentEngineBodyBytes = 8 << 20
	maxMemorySearchResults  = 10
)

// LedgerSessionOverview summarizes one Session Ledger record for zero-wake operator inspection.
type LedgerSessionOverview struct {
	SessionID    string         `json:"session_id"`
	AppName      string         `json:"app_name"`
	UserID       string         `json:"user_id"`
	IncidentID   string         `json:"incident_id"`
	ActorName    string         `json:"actor_name"`
	ActorState   string         `json:"actor_state"`
	ComputeBadge string         `json:"compute_badge"`
	Template     string         `json:"template"`
	IdleSeconds  int64          `json:"idle_seconds"`
	EventCount   int            `json:"event_count"`
	UpdatedAt    string         `json:"updated_at"`
	Backend      string         `json:"backend"`
	State        map[string]any `json:"state,omitempty"`
}

// LedgerEventCall summarizes one function call or response inside a Session Ledger event.
type LedgerEventCall struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Payload string `json:"payload,omitempty"`
}

// LedgerEventView formats one persisted session.Event for the /sessions inspector.
type LedgerEventView struct {
	ID            string            `json:"id"`
	InvocationID  string            `json:"invocation_id,omitempty"`
	Author        string            `json:"author"`
	Role          string            `json:"role,omitempty"`
	Kind          string            `json:"kind"`
	Summary       string            `json:"summary"`
	Text          string            `json:"text,omitempty"`
	ToolCalls     []LedgerEventCall `json:"tool_calls,omitempty"`
	ToolResponses []LedgerEventCall `json:"tool_responses,omitempty"`
	StateDelta    map[string]any    `json:"state_delta,omitempty"`
	Timestamp     string            `json:"timestamp"`
}

// LedgerSessionDetail returns full zero-wake session events and actor compute state.
type LedgerSessionDetail struct {
	LedgerSessionOverview
	Events []LedgerEventView `json:"events"`
}

// ConsolidatedMemoryFact represents a distilled cross-incident fact in Consolidated Memory.
type ConsolidatedMemoryFact struct {
	ID             string         `json:"id"`
	Fact           string         `json:"fact"`
	Content        *genai.Content `json:"content,omitempty"`
	Author         string         `json:"author"`
	AppName        string         `json:"app_name"`
	UserID         string         `json:"user_id"`
	SourceSession  string         `json:"source_session_id"`
	IncidentID     string         `json:"incident_id,omitempty"`
	Timestamp      time.Time      `json:"timestamp"`
	Topics         []string       `json:"topics,omitempty"`
	CustomMetadata map[string]any `json:"custom_metadata,omitempty"`

	words     map[string]struct{}
	textLower string
}

type storedLedgerSession struct {
	appName        string
	userID         string
	sessionID      string
	cloudSessionID string
	incidentID     string
	template       string
	state          map[string]any
	events         []*session.Event
	updatedAt      time.Time
}

type sessionWireDTO struct {
	AppName   string           `json:"app_name"`
	UserID    string           `json:"user_id"`
	SessionID string           `json:"session_id"`
	State     map[string]any   `json:"state,omitempty"`
	Events    []*session.Event `json:"events,omitempty"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// agentEngineProxy serves keyless /agentengine/* routes for Gemini Sessions
// (Session Ledger) and Gemini Memory Bank (Consolidated Memory). When a
// ReasoningEngine runtime ID is configured and MockLLM is false, it delegates
// over authenticated HTTPS to Gemini platform while keeping a
// synchronized durable broker index for zero-wake operator reads and fallback.
// sessionStateObserver receives notifications when durable broker session state changes.
type sessionStateObserver interface {
	savePersistedState()
	onBrokerSessionDeleted(sessionID string)
}

type agentEngineProxy struct {
	mockLLM       bool
	projectID     string
	region        string
	engineID      string
	autoBootstrap bool
	upstreamBase  string
	authedClient  *http.Client
	recordPolicy  func(PolicyEvent)
	observer      sessionStateObserver

	mu            sync.RWMutex
	sessions      map[string]*storedLedgerSession // "<appName>\x00<sessionID>" -> session
	sessionOrder  []string
	appState      map[string]map[string]any
	userState     map[string]map[string]map[string]any
	memoriesByApp map[string][]ConsolidatedMemoryFact // "<appName>\x00<userID>" -> facts
}

func normalizeReasoningEngineID(raw string) string {
	v := strings.TrimSpace(raw)
	switch strings.ToLower(v) {
	case "", "none", "local", "broker-durable", "unset", "auto":
		return ""
	}
	if idx := strings.LastIndex(v, "/reasoningEngines/"); idx >= 0 {
		v = v[idx+len("/reasoningEngines/"):]
	}
	return strings.Trim(v, "/")
}

func newAgentEngineProxy(ctx context.Context, cfg Config, observer sessionStateObserver, record func(PolicyEvent)) (*agentEngineProxy, error) {
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		if cfg.ModelLocation != "" && cfg.ModelLocation != "global" && cfg.ModelLocation != "us" && cfg.ModelLocation != "eu" {
			region = cfg.ModelLocation
		} else {
			region = "us-central1"
		}
	}
	wantAuto := !cfg.MockLLM && strings.EqualFold(strings.TrimSpace(cfg.ReasoningEngineID), "auto")
	engineID := normalizeReasoningEngineID(cfg.ReasoningEngineID)
	p := &agentEngineProxy{
		mockLLM:       cfg.MockLLM,
		projectID:     strings.TrimSpace(cfg.ProjectID),
		region:        region,
		engineID:      engineID,
		autoBootstrap: wantAuto,
		upstreamBase:  strings.TrimRight(strings.TrimSpace(cfg.AgentEngineUpstreamURL), "/"),
		recordPolicy:  record,
		observer:      observer,
		sessions:      make(map[string]*storedLedgerSession),
		appState:      make(map[string]map[string]any),
		userState:     make(map[string]map[string]map[string]any),
		memoriesByApp: make(map[string][]ConsolidatedMemoryFact),
	}
	if p.mockLLM || (p.engineID == "" && !p.autoBootstrap) {
		return p, nil
	}
	if p.upstreamBase != "" {
		p.authedClient = &http.Client{Timeout: 45 * time.Second}
		return p, nil
	}
	if p.projectID == "" {
		if p.autoBootstrap {
			p.autoBootstrap = false
			return p, nil
		}
		return nil, errors.New("GOOGLE_CLOUD_PROJECT is required when REASONING_ENGINE_ID is set and MOCK_LLM=false")
	}
	creds, err := credentials.DetectDefault(&credentials.DetectOptions{
		Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
	})
	if err != nil {
		if p.autoBootstrap {
			p.autoBootstrap = false
			return p, nil
		}
		return nil, fmt.Errorf("detect Workload Identity credentials for Gemini Sessions and Memory Bank: %w", err)
	}
	hc, err := httptransport.NewClient(&httptransport.Options{
		Credentials: creds,
	})
	if err != nil {
		if p.autoBootstrap {
			p.autoBootstrap = false
			return p, nil
		}
		return nil, fmt.Errorf("build authenticated HTTP client for Gemini Sessions and Memory Bank: %w", err)
	}
	hc.Timeout = 45 * time.Second
	p.authedClient = hc
	_ = ctx
	return p, nil
}

func (p *agentEngineProxy) getEngineID() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.engineID
}

func (p *agentEngineProxy) notifyMutated() {
	if p.observer != nil {
		p.observer.savePersistedState()
	}
}

func (p *agentEngineProxy) cloudEnabled() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cloudEnabledLocked()
}

func (p *agentEngineProxy) cloudEnabledLocked() bool {
	return !p.mockLLM && p.authedClient != nil && p.engineID != ""
}

func (p *agentEngineProxy) backendLabel() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.backendLabelLocked()
}

func (p *agentEngineProxy) backendLabelLocked() string {
	if p.cloudEnabledLocked() {
		return "gemini-platform"
	}
	return "broker-session-ledger"
}

func (p *agentEngineProxy) cloudBaseURL() string {
	if p.upstreamBase != "" {
		return p.upstreamBase
	}
	return fmt.Sprintf("https://%s-aiplatform.googleapis.com", p.region)
}

func (p *agentEngineProxy) cloudParentURL() string {
	return fmt.Sprintf("%s/v1beta1/projects/%s/locations/%s/reasoningEngines/%s", p.cloudBaseURL(), p.projectID, p.region, p.getEngineID())
}

func (p *agentEngineProxy) bootstrapReasoningEngine(ctx context.Context) {
	if p.authedClient == nil || p.projectID == "" || p.getEngineID() != "" {
		return
	}
	const wantDisplayName = "substrate-301-sre-memory"
	enginesURL := fmt.Sprintf("%s/v1beta1/projects/%s/locations/%s/reasoningEngines", p.cloudBaseURL(), p.projectID, p.region)

	var listOut struct {
		ReasoningEngines []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"reasoningEngines"`
	}
	if _, err := p.doCloudJSON(ctx, http.MethodGet, enginesURL, nil, &listOut); err == nil {
		for _, item := range listOut.ReasoningEngines {
			if item.DisplayName == wantDisplayName {
				if id := normalizeReasoningEngineID(item.Name); id != "" {
					p.mu.Lock()
					p.engineID = id
					p.mu.Unlock()
					p.notifyMutated()
					return
				}
			}
		}
	}

	var createOp struct {
		Name     string `json:"name"`
		Done     bool   `json:"done"`
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	createBody := map[string]string{
		"displayName": wantDisplayName,
		"description": "Managed session ledger and consolidated memory runtime for Agent Substrate on Gemini platform",
	}
	if _, err := p.doCloudJSON(ctx, http.MethodPost, enginesURL, createBody, &createOp); err != nil {
		return
	}
	if createOp.Done && createOp.Response.Name != "" {
		if id := normalizeReasoningEngineID(createOp.Response.Name); id != "" {
			p.mu.Lock()
			p.engineID = id
			p.mu.Unlock()
			p.notifyMutated()
		}
		return
	}
	opName := strings.TrimLeft(strings.TrimSpace(createOp.Name), "/")
	if opName == "" {
		return
	}
	pollDelay := 3 * time.Second
	if p.upstreamBase != "" {
		pollDelay = 20 * time.Millisecond
	}
	opURL := fmt.Sprintf("%s/v1beta1/%s", p.cloudBaseURL(), opName)
	for range 60 {
		timer := time.NewTimer(pollDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		var opRes struct {
			Done     bool `json:"done"`
			Response struct {
				Name string `json:"name"`
			} `json:"response"`
		}
		if _, err := p.doCloudJSON(ctx, http.MethodGet, opURL, nil, &opRes); err != nil {
			continue
		}
		if opRes.Done {
			if id := normalizeReasoningEngineID(opRes.Response.Name); id != "" {
				p.mu.Lock()
				p.engineID = id
				p.mu.Unlock()
				p.notifyMutated()
			}
			return
		}
	}
}

func sessionMapKey(appName, sessionID string) string {
	return appName + "\x00" + sessionID
}

func memoryMapKey(appName, userID string) string {
	return appName + "\x00" + userID
}

// StampSessionContext associates operational incident and template metadata with sessionID.
func (p *agentEngineProxy) StampSessionContext(appName, sessionID, incidentID, template string) {
	if strings.TrimSpace(appName) == "" {
		appName = agentruntime.AppName
	}
	key := sessionMapKey(appName, strings.TrimSpace(sessionID))
	p.mu.Lock()
	mutated := false
	if s, ok := p.sessions[key]; ok {
		if incidentID != "" {
			s.incidentID = strings.ToUpper(strings.TrimSpace(incidentID))
			mutated = true
		}
		if template != "" {
			s.template = strings.TrimSpace(template)
			mutated = true
		}
	}
	p.mu.Unlock()
	if mutated {
		p.notifyMutated()
	}
}

func (p *agentEngineProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAgentEngineBodyBytes))
	if err != nil {
		writeBrokerJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "agentengine payload exceeds limit"})
		return
	}

	route := strings.TrimPrefix(r.URL.Path, "/agentengine/")
	switch route {
	case "sessions:create":
		p.handleCreateSession(w, r, rawBody)
	case "sessions:get":
		p.handleGetSession(w, r, rawBody)
	case "sessions:list":
		p.handleListSessions(w, r, rawBody)
	case "sessions:delete":
		p.handleDeleteSession(w, r, rawBody)
	case "sessions:appendEvent":
		p.handleAppendEvent(w, r, rawBody)
	case "memories:addSession":
		p.handleAddSessionToMemory(w, r, rawBody)
	case "memories:search":
		p.handleSearchMemory(w, r, rawBody)
	default:
		http.NotFound(w, r)
	}
}

func (p *agentEngineProxy) handleCreateSession(w http.ResponseWriter, r *http.Request, raw []byte) {
	var req struct {
		AppName   string         `json:"app_name"`
		UserID    string         `json:"user_id"`
		SessionID string         `json:"session_id"`
		State     map[string]any `json:"state"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid create session JSON"})
		return
	}
	appName := strings.TrimSpace(req.AppName)
	userID := strings.TrimSpace(req.UserID)
	if appName == "" || userID == "" {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "app_name and user_id are required"})
		return
	}
	requestedID := strings.TrimSpace(req.SessionID)
	sessID := requestedID
	if sessID == "" {
		var err error
		sessID, err = randomHex(12)
		if err != nil {
			writeBrokerJSON(w, http.StatusInternalServerError, map[string]string{"error": "generate session_id failed"})
			return
		}
	}

	var cloudSessID string
	if p.cloudEnabled() {
		if cloudID, err := p.cloudCreateSession(r.Context(), sessID, userID, req.State); err == nil && cloudID != "" {
			cloudSessID = cloudID
			if requestedID == "" {
				sessID = cloudID
			}
		}
	}

	now := time.Now().UTC()
	key := sessionMapKey(appName, sessID)

	p.mu.Lock()
	if _, exists := p.sessions[key]; exists {
		p.mu.Unlock()
		writeBrokerJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("session %q already exists", sessID)})
		return
	}
	appDelta, userDelta, sessDelta := splitStateByScope(req.State)
	p.applyAppDeltaLocked(appName, appDelta)
	p.applyUserDeltaLocked(appName, userID, userDelta)

	incID := inferIncidentID(sessID, sessDelta)
	rec := &storedLedgerSession{
		appName:        appName,
		userID:         userID,
		sessionID:      sessID,
		cloudSessionID: cloudSessID,
		incidentID:     incID,
		state:          sessDelta,
		updatedAt:      now,
	}
	p.sessions[key] = rec
	p.sessionOrder = append(p.sessionOrder, key)
	merged := p.mergeStateLocked(appName, userID, rec.state)
	p.mu.Unlock()
	p.notifyMutated()

	p.emitPolicy("Session Ledger (/agentengine)", "CreateSession ("+sessID+")", "ALLOW",
		"Created managed session record via "+p.backendLabel())

	writeBrokerJSON(w, http.StatusOK, sessionWireDTO{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessID,
		State:     merged,
		UpdatedAt: now,
	})
}

func (p *agentEngineProxy) handleGetSession(w http.ResponseWriter, r *http.Request, raw []byte) {
	var req struct {
		AppName         string    `json:"app_name"`
		UserID          string    `json:"user_id"`
		SessionID       string    `json:"session_id"`
		NumRecentEvents int       `json:"num_recent_events"`
		After           time.Time `json:"after"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid get session JSON"})
		return
	}
	appName := strings.TrimSpace(req.AppName)
	userID := strings.TrimSpace(req.UserID)
	sessID := strings.TrimSpace(req.SessionID)
	if appName == "" || userID == "" || sessID == "" {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "app_name, user_id, and session_id are required"})
		return
	}

	key := sessionMapKey(appName, sessID)
	p.mu.RLock()
	stored, hasLocal := p.sessions[key]
	if hasLocal && stored.userID != userID {
		p.mu.RUnlock()
		writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("session %q not found", sessID)})
		return
	}
	var (
		localState   map[string]any
		localEvents  []*session.Event
		localUpdated time.Time
		cloudSessID  = sessID
	)
	if hasLocal {
		localState = p.mergeStateLocked(appName, userID, stored.state)
		localEvents = filterSessionEvents(stored.events, req.NumRecentEvents, req.After)
		localUpdated = stored.updatedAt
		if stored.cloudSessionID != "" {
			cloudSessID = stored.cloudSessionID
		}
	}
	p.mu.RUnlock()

	if p.cloudEnabled() {
		fullCloudDTO, resolvedCloudID, statusCode, err := p.cloudGetFullSession(r.Context(), appName, userID, cloudSessID)
		if err == nil {
			p.mu.Lock()
			cur, curOK := p.sessions[key]
			if curOK && cur.userID != userID {
				p.mu.Unlock()
				writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("session %q not found", sessID)})
				return
			}
			mutated := false
			if !curOK {
				cur = p.hydrateCloudSessionLocked(key, appName, userID, sessID, firstNonEmpty(resolvedCloudID, cloudSessID), fullCloudDTO)
				mutated = true
			} else {
				appDelta, userDelta, sessDelta := splitStateByScope(fullCloudDTO.State)
				p.applyAppDeltaLocked(appName, appDelta)
				p.applyUserDeltaLocked(appName, userID, userDelta)
				if len(fullCloudDTO.Events) > len(cur.events) {
					// Sync newer upstream ReasoningEngine events locally. When cur.events has
					// at least as many events, cur.events remains authoritative so ADK
					// EventActions (RequestedToolConfirmations) and append ordering stay intact.
					cur.events = slices.Clone(fullCloudDTO.Events)
					if fullCloudDTO.UpdatedAt.After(cur.updatedAt) {
						cur.updatedAt = fullCloudDTO.UpdatedAt
					}
					if len(sessDelta) > 0 {
						if cur.state == nil {
							cur.state = make(map[string]any)
						}
						for k, v := range sessDelta {
							if _, exists := cur.state[k]; !exists {
								cur.state[k] = v
							}
						}
					}
					mutated = true
				}
			}

			respEvents := filterSessionEvents(cur.events, req.NumRecentEvents, req.After)
			respState := p.mergeStateLocked(appName, userID, cur.state)
			respUpdated := cur.updatedAt
			if fullCloudDTO.UpdatedAt.After(respUpdated) {
				respUpdated = fullCloudDTO.UpdatedAt
			}
			p.mu.Unlock()
			if mutated {
				p.notifyMutated()
			}
			writeBrokerJSON(w, http.StatusOK, sessionWireDTO{
				AppName:   appName,
				UserID:    userID,
				SessionID: sessID,
				State:     respState,
				Events:    respEvents,
				UpdatedAt: respUpdated,
			})
			return
		}
		if statusCode == http.StatusNotFound && !hasLocal {
			writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("session %q not found", sessID)})
			return
		}
	}

	if !hasLocal {
		writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("session %q not found", sessID)})
		return
	}

	writeBrokerJSON(w, http.StatusOK, sessionWireDTO{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessID,
		State:     localState,
		Events:    localEvents,
		UpdatedAt: localUpdated,
	})
}

func (p *agentEngineProxy) handleListSessions(w http.ResponseWriter, _ *http.Request, raw []byte) {
	var req struct {
		AppName string `json:"app_name"`
		UserID  string `json:"user_id"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid list sessions JSON"})
		return
	}
	appName := strings.TrimSpace(req.AppName)
	userID := strings.TrimSpace(req.UserID)
	if appName == "" {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "app_name is required"})
		return
	}

	p.mu.RLock()
	out := make([]sessionWireDTO, 0)
	for _, key := range p.sessionOrder {
		stored, ok := p.sessions[key]
		if !ok || stored.appName != appName {
			continue
		}
		if userID != "" && stored.userID != userID {
			continue
		}
		out = append(out, sessionWireDTO{
			AppName:   stored.appName,
			UserID:    stored.userID,
			SessionID: stored.sessionID,
			State:     p.mergeStateLocked(stored.appName, stored.userID, stored.state),
			UpdatedAt: stored.updatedAt,
		})
	}
	p.mu.RUnlock()

	writeBrokerJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (p *agentEngineProxy) handleDeleteSession(w http.ResponseWriter, r *http.Request, raw []byte) {
	var req struct {
		AppName   string `json:"app_name"`
		UserID    string `json:"user_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delete session JSON"})
		return
	}
	appName := strings.TrimSpace(req.AppName)
	userID := strings.TrimSpace(req.UserID)
	sessID := strings.TrimSpace(req.SessionID)
	if appName == "" || userID == "" || sessID == "" {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "app_name, user_id, and session_id are required"})
		return
	}

	key := sessionMapKey(appName, sessID)
	deleted := false
	cloudSessID := sessID
	p.mu.Lock()
	if stored, ok := p.sessions[key]; ok && stored.userID == userID {
		if stored.cloudSessionID != "" {
			cloudSessID = stored.cloudSessionID
		}
		delete(p.sessions, key)
		p.sessionOrder = slices.DeleteFunc(p.sessionOrder, func(k string) bool { return k == key })
		deleted = true
	}
	p.mu.Unlock()
	if p.observer != nil {
		p.observer.onBrokerSessionDeleted(sessID)
	}
	if deleted {
		p.notifyMutated()
	}

	if p.cloudEnabled() {
		_ = p.cloudDeleteSession(r.Context(), sessID, cloudSessID, userID)
	}

	writeBrokerJSON(w, http.StatusOK, map[string]string{"deleted": sessID})
}

func (p *agentEngineProxy) handleAppendEvent(w http.ResponseWriter, r *http.Request, raw []byte) {
	var req struct {
		AppName   string         `json:"app_name"`
		UserID    string         `json:"user_id"`
		SessionID string         `json:"session_id"`
		Event     *session.Event `json:"event"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid appendEvent JSON"})
		return
	}
	appName := strings.TrimSpace(req.AppName)
	userID := strings.TrimSpace(req.UserID)
	sessID := strings.TrimSpace(req.SessionID)
	if appName == "" || sessID == "" || req.Event == nil {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "app_name, session_id, and event are required"})
		return
	}
	if req.Event.Partial {
		writeBrokerJSON(w, http.StatusOK, map[string]string{"status": "skipped_partial"})
		return
	}
	if req.Event.ID == "" {
		idHex, _ := randomHex(12)
		req.Event.ID = firstNonEmpty(idHex, fmt.Sprintf("ev-%d", time.Now().UnixNano()))
	}
	if req.Event.Timestamp.IsZero() {
		req.Event.Timestamp = time.Now().UTC()
	}

	cleanEv, err := cloneAndStripTempEvent(req.Event)
	if err != nil {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "encode session event failed"})
		return
	}

	key := sessionMapKey(appName, sessID)
	p.mu.RLock()
	existingRec, hasLocal := p.sessions[key]
	if hasLocal && userID != "" && existingRec.userID != userID {
		p.mu.RUnlock()
		writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("session %q not found", sessID)})
		return
	}
	cloudSessID := sessID
	localUser := userID
	var localState map[string]any
	if hasLocal {
		if existingRec.cloudSessionID != "" {
			cloudSessID = existingRec.cloudSessionID
		}
		localUser = existingRec.userID
		localState = maps.Clone(existingRec.state)
	}
	p.mu.RUnlock()

	var (
		hydratedFromCloud bool
		preCloudDTO       sessionWireDTO
		preCloudID        string
	)
	if p.cloudEnabled() {
		if !hasLocal {
			dto, resolvedID, preCode, preErr := p.cloudGetFullSession(r.Context(), appName, firstNonEmpty(userID, "sre-operator"), cloudSessID)
			if preErr != nil {
				if preCode == http.StatusNotFound {
					writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("session %q not found", sessID)})
					return
				}
				outCode := preCode
				if outCode < 400 {
					outCode = http.StatusBadGateway
				}
				writeBrokerJSON(w, outCode, map[string]string{"error": preErr.Error()})
				return
			}
			hydratedFromCloud = true
			preCloudDTO = dto
			preCloudID = resolvedID
			if resolvedID != "" {
				cloudSessID = resolvedID
			}
		}
		statusCode, cloudErr := p.cloudAppendEvent(r.Context(), cloudSessID, cleanEv)
		if cloudErr != nil {
			if statusCode == http.StatusNotFound && hasLocal {
				if newCloudID, createErr := p.cloudCreateSession(r.Context(), sessID, firstNonEmpty(localUser, "sre-operator"), localState); createErr == nil && newCloudID != "" {
					p.mu.Lock()
					if cur, ok := p.sessions[key]; ok {
						cur.cloudSessionID = newCloudID
					}
					p.mu.Unlock()
					_, _ = p.cloudAppendEvent(r.Context(), newCloudID, cleanEv)
				}
			} else if !hasLocal {
				outCode := statusCode
				if outCode < 400 {
					outCode = http.StatusBadGateway
				}
				writeBrokerJSON(w, outCode, map[string]string{"error": cloudErr.Error()})
				return
			}
		}
	}

	p.mu.Lock()
	stored, ok := p.sessions[key]
	if !ok && hydratedFromCloud {
		effUser := firstNonEmpty(userID, preCloudDTO.UserID, "sre-operator")
		stored = p.hydrateCloudSessionLocked(key, appName, effUser, sessID, firstNonEmpty(preCloudID, cloudSessID), preCloudDTO)
		ok = true
	}
	if !ok || (userID != "" && stored.userID != userID) {
		p.mu.Unlock()
		writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("session %q not found", sessID)})
		return
	}
	stored.events = append(stored.events, cleanEv)
	stored.updatedAt = cleanEv.Timestamp
	if len(cleanEv.Actions.StateDelta) > 0 {
		appDelta, userDelta, sessDelta := splitStateByScope(cleanEv.Actions.StateDelta)
		p.applyAppDeltaLocked(appName, appDelta)
		p.applyUserDeltaLocked(appName, stored.userID, userDelta)
		if stored.state == nil {
			stored.state = make(map[string]any)
		}
		maps.Copy(stored.state, sessDelta)
	}
	if stored.incidentID == "" {
		stored.incidentID = inferIncidentIDFromEvent(sessID, cleanEv)
	}
	p.mu.Unlock()
	p.notifyMutated()

	p.emitPolicy("Session Ledger (/agentengine)", "AppendEvent ("+sessID+")", "ALLOW",
		"Persisted "+describeEventKind(cleanEv)+" event ("+cleanEv.ID+") to "+p.backendLabel())

	writeBrokerJSON(w, http.StatusOK, map[string]string{"id": cleanEv.ID})
}

func (p *agentEngineProxy) handleAddSessionToMemory(w http.ResponseWriter, r *http.Request, raw []byte) {
	var req struct {
		AppName   string           `json:"app_name"`
		UserID    string           `json:"user_id"`
		SessionID string           `json:"session_id"`
		State     map[string]any   `json:"state"`
		Events    []*session.Event `json:"events"`
		UpdatedAt time.Time        `json:"updated_at"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid addSession memory JSON"})
		return
	}
	appName := firstNonEmpty(req.AppName, agentruntime.AppName)
	userID := firstNonEmpty(req.UserID, "sre-operator")
	sessID := strings.TrimSpace(req.SessionID)
	if sessID == "" {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "session_id is required"})
		return
	}

	events := req.Events
	if len(events) == 0 {
		p.mu.RLock()
		if stored, ok := p.sessions[sessionMapKey(appName, sessID)]; ok {
			events = slices.Clone(stored.events)
		}
		p.mu.RUnlock()
	}

	if p.cloudEnabled() {
		_ = p.cloudGenerateMemories(r.Context(), userID, events)
	}

	extracted := extractConsolidatedFacts(appName, userID, sessID, req.State, events, req.UpdatedAt)
	mKey := memoryMapKey(appName, userID)

	p.mu.Lock()
	existing := p.memoriesByApp[mKey]
	filtered := make([]ConsolidatedMemoryFact, 0, len(existing)+len(extracted))
	for _, item := range existing {
		if item.SourceSession != sessID {
			filtered = append(filtered, item)
		}
	}
	filtered = append(filtered, extracted...)
	p.memoriesByApp[mKey] = filtered
	p.mu.Unlock()
	p.notifyMutated()

	p.emitPolicy("Consolidated Memory (/agentengine)", "AddSessionToMemory ("+sessID+")", "ALLOW",
		fmt.Sprintf("Extracted %d consolidated root-cause/operational fact(s) into %s", len(extracted), p.backendLabel()))

	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"session_id":      sessID,
		"extracted_facts": len(extracted),
	})
}

func (p *agentEngineProxy) handleSearchMemory(w http.ResponseWriter, r *http.Request, raw []byte) {
	var req struct {
		AppName string `json:"app_name"`
		UserID  string `json:"user_id"`
		Query   string `json:"query"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid search memory JSON"})
		return
	}
	appName := firstNonEmpty(req.AppName, agentruntime.AppName)
	userID := firstNonEmpty(req.UserID, "sre-operator")
	matches := p.searchMemories(r.Context(), appName, userID, req.Query)

	p.emitPolicy("Consolidated Memory (/agentengine)", "SearchMemory", "ALLOW",
		fmt.Sprintf("Query %q matched %d consolidated memory fact(s)", truncateText(req.Query, 64), len(matches)))

	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"memories": matches,
	})
}

func (p *agentEngineProxy) searchMemories(ctx context.Context, appName, userID, query string) []ConsolidatedMemoryFact {
	var results []ConsolidatedMemoryFact
	seenFacts := map[string]bool{}

	if p.cloudEnabled() {
		if cloudFacts, err := p.cloudRetrieveMemories(ctx, appName, userID, query); err == nil {
			for _, f := range cloudFacts {
				norm := strings.ToLower(strings.TrimSpace(f.Fact))
				if norm != "" && !seenFacts[norm] {
					seenFacts[norm] = true
					results = append(results, f)
				}
			}
		}
	}

	p.mu.RLock()
	var pool []ConsolidatedMemoryFact
	if userID != "" {
		pool = append(pool, p.memoriesByApp[memoryMapKey(appName, userID)]...)
	} else {
		prefix := appName + "\x00"
		for k, facts := range p.memoriesByApp {
			if strings.HasPrefix(k, prefix) {
				pool = append(pool, facts...)
			}
		}
	}
	p.mu.RUnlock()

	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		for i := len(pool) - 1; i >= 0; i-- {
			norm := strings.ToLower(strings.TrimSpace(pool[i].Fact))
			if norm != "" && !seenFacts[norm] {
				seenFacts[norm] = true
				results = append(results, pool[i])
			}
		}
		return results
	}

	qWords := expandMemoryQueryWords(trimmedQuery)
	if len(qWords) == 0 {
		return results
	}

	type scoredFact struct {
		score int
		fact  ConsolidatedMemoryFact
	}
	var scored []scoredFact
	for _, cand := range pool {
		norm := strings.ToLower(strings.TrimSpace(cand.Fact))
		if norm == "" || seenFacts[norm] {
			continue
		}
		score := 0
		for qw := range qWords {
			if _, ok := cand.words[qw]; ok || strings.Contains(cand.textLower, qw) {
				score++
			}
		}
		if score > 0 {
			scored = append(scored, scoredFact{score: score, fact: cand})
		}
	}
	slices.SortStableFunc(scored, func(a, b scoredFact) int {
		return cmp.Compare(b.score, a.score)
	})
	for _, item := range scored {
		if len(results) >= maxMemorySearchResults {
			break
		}
		seenFacts[strings.ToLower(strings.TrimSpace(item.fact.Fact))] = true
		results = append(results, item.fact)
	}
	return results
}

// ListLedgerSessions returns zero-wake session summaries paired with live Actor control-plane badges.
func (p *agentEngineProxy) ListLedgerSessions(actorsByName map[string]ActorSummary, defaultTemplate string) []LedgerSessionOverview {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]LedgerSessionOverview, 0, len(p.sessionOrder))
	for i := len(p.sessionOrder) - 1; i >= 0; i-- {
		stored, ok := p.sessions[p.sessionOrder[i]]
		if !ok {
			continue
		}
		out = append(out, p.buildSessionOverviewLocked(stored, actorsByName, defaultTemplate))
	}
	return out
}

// GetLedgerSessionDetail returns full zero-wake structured events for sessionID without touching atenet-router.
func (p *agentEngineProxy) GetLedgerSessionDetail(sessionID string, actorsByName map[string]ActorSummary, defaultTemplate string) (LedgerSessionDetail, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var target *storedLedgerSession
	for _, key := range p.sessionOrder {
		if s, ok := p.sessions[key]; ok && s.sessionID == sessionID {
			target = s
			break
		}
	}
	if target == nil {
		return LedgerSessionDetail{}, false
	}

	overview := p.buildSessionOverviewLocked(target, actorsByName, defaultTemplate)
	views := make([]LedgerEventView, 0, len(target.events))
	for _, ev := range target.events {
		if ev != nil {
			views = append(views, formatLedgerEventView(ev))
		}
	}
	return LedgerSessionDetail{
		LedgerSessionOverview: overview,
		Events:                views,
	}, true
}

func (p *agentEngineProxy) buildSessionOverviewLocked(stored *storedLedgerSession, actorsByName map[string]ActorSummary, defaultTemplate string) LedgerSessionOverview {
	incID := firstNonEmpty(stored.incidentID, strings.ToUpper(stored.sessionID))
	actorName := incidentToActorName(incID)
	if _, exists := actorsByName[stored.sessionID]; exists {
		actorName = stored.sessionID
	}

	state := "NOT_CREATED"
	badge := "Deleted / upgraded · Session Ledger retained (0 vCPU)"
	tmpl := firstNonEmpty(stored.template, defaultTemplate)
	var idleSec int64
	if summary, exists := actorsByName[actorName]; exists {
		state = summary.State
		badge = summary.ComputeBadge
		tmpl = firstNonEmpty(summary.Template, tmpl)
		idleSec = summary.IdleSeconds
	}

	return LedgerSessionOverview{
		SessionID:    stored.sessionID,
		AppName:      stored.appName,
		UserID:       stored.userID,
		IncidentID:   incID,
		ActorName:    actorName,
		ActorState:   state,
		ComputeBadge: badge,
		Template:     tmpl,
		IdleSeconds:  idleSec,
		EventCount:   len(stored.events),
		UpdatedAt:    stored.updatedAt.UTC().Format(time.RFC3339),
		Backend:      p.backendLabelLocked(),
		State:        p.mergeStateLocked(stored.appName, stored.userID, stored.state),
	}
}

func (p *agentEngineProxy) emitPolicy(boundary, action, verdict, detail string) {
	if p.recordPolicy == nil {
		return
	}
	p.recordPolicy(PolicyEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Boundary:  boundary,
		Action:    action,
		Verdict:   verdict,
		Detail:    detail,
	})
}

func (p *agentEngineProxy) applyAppDeltaLocked(appName string, delta map[string]any) {
	if len(delta) == 0 {
		return
	}
	m, ok := p.appState[appName]
	if !ok {
		m = make(map[string]any)
		p.appState[appName] = m
	}
	maps.Copy(m, delta)
}

func (p *agentEngineProxy) applyUserDeltaLocked(appName, userID string, delta map[string]any) {
	if len(delta) == 0 {
		return
	}
	byUser, ok := p.userState[appName]
	if !ok {
		byUser = make(map[string]map[string]any)
		p.userState[appName] = byUser
	}
	m, ok := byUser[userID]
	if !ok {
		m = make(map[string]any)
		byUser[userID] = m
	}
	maps.Copy(m, delta)
}

func (p *agentEngineProxy) mergeStateLocked(appName, userID string, sessState map[string]any) map[string]any {
	merged := make(map[string]any)
	for k, v := range p.appState[appName] {
		merged[session.KeyPrefixApp+k] = v
	}
	if byUser, ok := p.userState[appName]; ok {
		for k, v := range byUser[userID] {
			merged[session.KeyPrefixUser+k] = v
		}
	}
	for k, v := range sessState {
		merged[k] = v
	}
	return merged
}

func splitStateByScope(in map[string]any) (appDelta, userDelta, sessDelta map[string]any) {
	sessDelta = make(map[string]any)
	for k, v := range in {
		switch {
		case strings.HasPrefix(k, session.KeyPrefixTemp):
			continue
		case strings.HasPrefix(k, session.KeyPrefixApp):
			if appDelta == nil {
				appDelta = make(map[string]any)
			}
			appDelta[strings.TrimPrefix(k, session.KeyPrefixApp)] = v
		case strings.HasPrefix(k, session.KeyPrefixUser):
			if userDelta == nil {
				userDelta = make(map[string]any)
			}
			userDelta[strings.TrimPrefix(k, session.KeyPrefixUser)] = v
		default:
			sessDelta[k] = v
		}
	}
	return appDelta, userDelta, sessDelta
}

func cloneAndStripTempEvent(ev *session.Event) (*session.Event, error) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	var cloned session.Event
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	if len(cloned.Actions.StateDelta) > 0 {
		filtered := make(map[string]any, len(cloned.Actions.StateDelta))
		for k, v := range cloned.Actions.StateDelta {
			if !strings.HasPrefix(k, session.KeyPrefixTemp) {
				filtered[k] = v
			}
		}
		cloned.Actions.StateDelta = filtered
	}
	return &cloned, nil
}

func filterSessionEvents(events []*session.Event, numRecent int, after time.Time) []*session.Event {
	filtered := events
	if numRecent > 0 {
		start := max(len(filtered)-numRecent, 0)
		filtered = filtered[start:]
	}
	if !after.IsZero() && len(filtered) > 0 {
		firstIdx := sort.Search(len(filtered), func(i int) bool {
			return !filtered[i].Timestamp.Before(after)
		})
		filtered = filtered[firstIdx:]
	}
	out := make([]*session.Event, 0, len(filtered))
	out = append(out, filtered...)
	return out
}

func describeEventKind(ev *session.Event) string {
	if ev == nil {
		return "event"
	}
	if ev.Actions.Compaction != nil {
		return "compaction"
	}
	if ev.Content != nil {
		for _, p := range ev.Content.Parts {
			if p == nil {
				continue
			}
			if p.FunctionCall != nil {
				return "tool_call"
			}
			if p.FunctionResponse != nil {
				return "tool_response"
			}
		}
		if ev.Author == "user" || ev.Content.Role == genai.RoleUser {
			return "prompt"
		}
		return "agent_reply"
	}
	if len(ev.Actions.StateDelta) > 0 {
		return "state_delta"
	}
	return "event"
}

func formatLedgerEventView(ev *session.Event) LedgerEventView {
	kind := describeEventKind(ev)
	var (
		role          string
		texts         []string
		toolCalls     []LedgerEventCall
		toolResponses []LedgerEventCall
	)
	if ev.Content != nil {
		role = ev.Content.Role
		for _, pt := range ev.Content.Parts {
			if pt == nil {
				continue
			}
			if pt.Text != "" {
				texts = append(texts, strings.TrimSpace(pt.Text))
			}
			if pt.FunctionCall != nil {
				argsRaw, _ := json.Marshal(pt.FunctionCall.Args)
				toolCalls = append(toolCalls, LedgerEventCall{
					ID:      pt.FunctionCall.ID,
					Name:    pt.FunctionCall.Name,
					Payload: string(argsRaw),
				})
			}
			if pt.FunctionResponse != nil {
				respRaw, _ := json.Marshal(pt.FunctionResponse.Response)
				toolResponses = append(toolResponses, LedgerEventCall{
					ID:      pt.FunctionResponse.ID,
					Name:    pt.FunctionResponse.Name,
					Payload: truncateText(string(respRaw), 1200),
				})
			}
		}
	}

	joinedText := strings.Join(texts, "\n")
	summary := joinedText
	switch {
	case len(toolCalls) > 0:
		names := make([]string, 0, len(toolCalls))
		for _, tc := range toolCalls {
			names = append(names, tc.Name)
		}
		summary = "Tool call: " + strings.Join(names, ", ")
	case len(toolResponses) > 0:
		names := make([]string, 0, len(toolResponses))
		for _, tr := range toolResponses {
			names = append(names, tr.Name)
		}
		summary = "Tool response: " + strings.Join(names, ", ")
	case summary == "" && len(ev.Actions.StateDelta) > 0:
		keys := make([]string, 0, len(ev.Actions.StateDelta))
		for k := range ev.Actions.StateDelta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		summary = "State delta: " + strings.Join(keys, ", ")
	}
	summary = truncateText(strings.ReplaceAll(summary, "\n", " "), 120)

	return LedgerEventView{
		ID:            ev.ID,
		InvocationID:  ev.InvocationID,
		Author:        ev.Author,
		Role:          role,
		Kind:          kind,
		Summary:       summary,
		Text:          joinedText,
		ToolCalls:     toolCalls,
		ToolResponses: toolResponses,
		StateDelta:    ev.Actions.StateDelta,
		Timestamp:     ev.Timestamp.UTC().Format(time.RFC3339),
	}
}

func extractConsolidatedFacts(appName, userID, sessionID string, state map[string]any, events []*session.Event, updatedAt time.Time) []ConsolidatedMemoryFact {
	ts := updatedAt
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	incID := inferIncidentID(sessionID, state)

	var (
		allTextBuilder strings.Builder
		modelReplies   []string
	)
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if incID == "" {
			incID = inferIncidentIDFromEvent(sessionID, ev)
		}
		if ev.Content == nil {
			continue
		}
		for _, pt := range ev.Content.Parts {
			if pt == nil {
				continue
			}
			if pt.Text != "" {
				allTextBuilder.WriteString(pt.Text + "\n")
				if ev.Author != "user" && ev.Content.Role == genai.RoleModel {
					modelReplies = append(modelReplies, strings.TrimSpace(pt.Text))
				}
			}
			if pt.FunctionResponse != nil {
				raw, _ := json.Marshal(pt.FunctionResponse.Response)
				allTextBuilder.WriteString(string(raw) + "\n")
			}
		}
	}
	if incID == "" {
		incID = strings.ToUpper(sessionID)
	}

	corpus := strings.ToLower(allTextBuilder.String())
	var facts []ConsolidatedMemoryFact

	sawPoolCap := strings.Contains(corpus, "max_open_conns") || strings.Contains(corpus, "idle_timeout_seconds")
	sawCursorLeak := strings.Contains(corpus, "reconciletransfer") || strings.Contains(corpus, "validatechecksum") || strings.Contains(corpus, "rows.close")

	if sawPoolCap {
		factText := fmt.Sprintf(
			"[%s · ledger-api connection pool sizing] config/pool.yaml sets MAX_OPEN_CONNS=2 (max_open_conns: 2) and IDLE_TIMEOUT_SECONDS=0 (idle_timeout_seconds: 0), exhausting all pool slots once two cursors stall. Validated pool tuning raises max_open_conns to 32 and idle_timeout_seconds to 30.",
			incID,
		)
		facts = append(facts, buildConsolidatedFact(
			fmt.Sprintf("mem-%s-pool-config", sessionID),
			factText,
			appName,
			userID,
			sessionID,
			incID,
			ts,
			[]string{"ledger-api", "connection-pool", "config/pool.yaml", incID},
		))
	}

	if sawCursorLeak {
		factText := fmt.Sprintf(
			"[%s · ledger-api cursor leak] pool/store.go ReconcileTransfer calls rows.ValidateChecksum() before rows.Close(), leaking semaphore slots on bad-* checksum batches and causing HTTP 503 pool_exhausted. Add defer rows.Close() immediately after AcquireRows.",
			incID,
		)
		facts = append(facts, buildConsolidatedFact(
			fmt.Sprintf("mem-%s-cursor-leak", sessionID),
			factText,
			appName,
			userID,
			sessionID,
			incID,
			ts,
			[]string{"ledger-api", "cursor-leak", "pool/store.go", incID},
		))
	}

	if len(modelReplies) > 0 {
		lastReply := modelReplies[len(modelReplies)-1]
		if len(facts) == 0 || (!sawPoolCap && !sawCursorLeak) {
			facts = append(facts, buildConsolidatedFact(
				fmt.Sprintf("mem-%s-summary", sessionID),
				fmt.Sprintf("[%s · session summary] %s", incID, truncateText(lastReply, 420)),
				appName,
				userID,
				sessionID,
				incID,
				ts,
				[]string{incID, "triage-summary"},
			))
		}
	}

	if len(facts) == 0 && strings.TrimSpace(allTextBuilder.String()) != "" {
		facts = append(facts, buildConsolidatedFact(
			fmt.Sprintf("mem-%s-note", sessionID),
			fmt.Sprintf("[%s · session note] %s", incID, truncateText(strings.TrimSpace(allTextBuilder.String()), 320)),
			appName,
			userID,
			sessionID,
			incID,
			ts,
			[]string{incID},
		))
	}

	return facts
}

func buildConsolidatedFact(id, factText, appName, userID, sessionID, incidentID string, ts time.Time, topics []string) ConsolidatedMemoryFact {
	meta := map[string]any{
		"source_session_id": sessionID,
		"incident_id":       incidentID,
		"topics":            topics,
	}
	indexBlob := factText + " " + sessionID + " " + incidentID + " " + strings.Join(topics, " ")
	return ConsolidatedMemoryFact{
		ID:             id,
		Fact:           factText,
		Content:        genai.NewContentFromText(factText, genai.RoleUser),
		Author:         "consolidated_memory",
		AppName:        appName,
		UserID:         userID,
		SourceSession:  sessionID,
		IncidentID:     incidentID,
		Timestamp:      ts,
		Topics:         topics,
		CustomMetadata: meta,
		words:          tokenizeMemoryWords(indexBlob),
		textLower:      strings.ToLower(indexBlob),
	}
}

func tokenizeMemoryWords(text string) map[string]struct{} {
	out := make(map[string]struct{})
	for word := range strings.FieldsFuncSeq(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '_' && r != '-'
	}) {
		w := strings.ToLower(strings.Trim(word, "-_"))
		if len(w) >= 2 {
			out[w] = struct{}{}
		}
	}
	return out
}

func expandMemoryQueryWords(query string) map[string]struct{} {
	words := tokenizeMemoryWords(query)
	if _, ok := words["pool"]; ok || strings.Contains(strings.ToLower(query), "503") || strings.Contains(strings.ToLower(query), "inc-403") {
		words["ledger-api"] = struct{}{}
		words["max_open_conns"] = struct{}{}
		words["connection"] = struct{}{}
	}
	return words
}

const (
	sessionStateKeyWorkshopID = "workshop_session_id"
	sessionStateKeyIncidentID = "incident_id"
)

var workshopIncidentIDs = [...]string{"INC-402", "INC-403", "INC-409"}

func inferIncidentID(sessionID string, state map[string]any) string {
	if state != nil {
		if v, ok := state[sessionStateKeyIncidentID].(string); ok && strings.TrimSpace(v) != "" {
			return strings.ToUpper(strings.TrimSpace(v))
		}
	}
	upper := strings.ToUpper(strings.TrimSpace(sessionID))
	if strings.HasPrefix(upper, "INC-") {
		return upper
	}
	return ""
}

func inferIncidentIDFromEvent(sessionID string, ev *session.Event) string {
	if id := inferIncidentID(sessionID, ev.Actions.StateDelta); id != "" {
		return id
	}
	if ev.Content != nil {
		for _, p := range ev.Content.Parts {
			if p == nil {
				continue
			}
			upper := strings.ToUpper(p.Text)
			bestCand := ""
			bestIdx := -1
			for _, cand := range workshopIncidentIDs {
				if idx := strings.Index(upper, cand); idx >= 0 && (bestIdx < 0 || idx < bestIdx) {
					bestIdx = idx
					bestCand = cand
				}
			}
			if bestCand != "" {
				return bestCand
			}
		}
	}
	return ""
}

func truncateText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// --- Managed Gemini platform REST Delegation ---

func (p *agentEngineProxy) cloudCreateSession(ctx context.Context, sessionID, userID string, state map[string]any) (string, error) {
	endpoint := p.cloudParentURL() + "/sessions"
	if sessionID != "" {
		endpoint += "?sessionId=" + url.QueryEscape(sessionID)
	}
	cloudState := maps.Clone(state)
	if sessionID != "" || len(cloudState) > 0 {
		if cloudState == nil {
			cloudState = make(map[string]any, 2)
		}
		if sessionID != "" && stateStringField(cloudState, sessionStateKeyWorkshopID) == "" {
			cloudState[sessionStateKeyWorkshopID] = sessionID
		}
		if incID := inferIncidentID(sessionID, state); incID != "" && stateStringField(cloudState, sessionStateKeyIncidentID) == "" {
			cloudState[sessionStateKeyIncidentID] = incID
		}
	}
	body := map[string]any{
		"userId": userID,
	}
	if len(cloudState) > 0 {
		body["sessionState"] = cloudState
	}
	var opResp struct {
		Name     string `json:"name"`
		Done     bool   `json:"done"`
		Response struct {
			Name string `json:"name"`
		} `json:"response"`
	}
	if _, err := p.doCloudJSON(ctx, http.MethodPost, endpoint, body, &opResp); err != nil {
		return "", err
	}
	if opResp.Response.Name != "" {
		if idx := strings.LastIndex(opResp.Response.Name, "/sessions/"); idx >= 0 {
			rest := opResp.Response.Name[idx+len("/sessions/"):]
			if slash := strings.IndexByte(rest, '/'); slash >= 0 {
				rest = rest[:slash]
			}
			if rest != "" {
				return rest, nil
			}
		}
	}
	if idx := strings.LastIndex(opResp.Name, "/sessions/"); idx >= 0 {
		rest := opResp.Name[idx+len("/sessions/"):]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			rest = rest[:slash]
		}
		if rest != "" {
			return rest, nil
		}
	}
	return sessionID, nil
}

// maxCloudEventPages bounds pagination over ReasoningEngine /events (100 events
// per page -> up to 2,000 events) so malformed nextPageToken responses cannot
// stall a turn indefinitely while covering multi-run workshop session histories.
const maxCloudEventPages = 20

func (p *agentEngineProxy) hydrateCloudSessionLocked(
	key, appName, userID, sessID, cloudSessID string,
	dto sessionWireDTO,
) *storedLedgerSession {
	appDelta, userDelta, sessDelta := splitStateByScope(dto.State)
	p.applyAppDeltaLocked(appName, appDelta)
	p.applyUserDeltaLocked(appName, userID, userDelta)

	incID := inferIncidentID(sessID, sessDelta)
	for _, ev := range dto.Events {
		if incID != "" {
			break
		}
		if ev != nil {
			incID = inferIncidentIDFromEvent(sessID, ev)
		}
	}
	updatedAt := dto.UpdatedAt
	if updatedAt.IsZero() && len(dto.Events) > 0 {
		updatedAt = dto.Events[len(dto.Events)-1].Timestamp
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	rec := &storedLedgerSession{
		appName:        appName,
		userID:         userID,
		sessionID:      sessID,
		cloudSessionID: cloudSessID,
		incidentID:     incID,
		state:          sessDelta,
		events:         slices.Clone(dto.Events),
		updatedAt:      updatedAt,
	}
	p.sessions[key] = rec
	if !slices.Contains(p.sessionOrder, key) {
		p.sessionOrder = append(p.sessionOrder, key)
	}
	return rec
}

func extractCloudSessionIDFromResource(resourceName, fallback string) string {
	if idx := strings.LastIndex(resourceName, "/sessions/"); idx >= 0 {
		rest := resourceName[idx+len("/sessions/"):]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			rest = rest[:slash]
		}
		if rest != "" {
			return rest
		}
	}
	return fallback
}

type cloudSessionEventWire struct {
	Name         string          `json:"name"`
	Author       string          `json:"author"`
	InvocationID string          `json:"invocationId"`
	Timestamp    time.Time       `json:"timestamp"`
	Content      *genai.Content  `json:"content"`
	RawEvent     json.RawMessage `json:"rawEvent"`
}

func decodeCloudSessionEvent(ce cloudSessionEventWire) *session.Event {
	var ev session.Event
	if len(ce.RawEvent) > 0 && string(ce.RawEvent) != "null" {
		_ = json.Unmarshal(ce.RawEvent, &ev)
	} else {
		ev = session.Event{
			Author:       ce.Author,
			InvocationID: ce.InvocationID,
			Timestamp:    ce.Timestamp,
			LLMResponse:  model.LLMResponse{Content: ce.Content},
		}
	}
	if ev.Content == nil && ce.Content != nil {
		ev.Content = ce.Content
	}
	if idx := strings.LastIndexByte(ce.Name, '/'); idx >= 0 && idx+1 < len(ce.Name) {
		ev.ID = ce.Name[idx+1:]
	}
	if !ce.Timestamp.IsZero() {
		ev.Timestamp = ce.Timestamp
	}
	if ce.Author != "" {
		ev.Author = ce.Author
	}
	if ce.InvocationID != "" {
		ev.InvocationID = ce.InvocationID
	}
	reconstructToolConfirmationActions(&ev)
	return &ev
}

func (p *agentEngineProxy) cloudGetFullSession(ctx context.Context, appName, userID, sessionID string) (sessionWireDTO, string, int, error) {
	sessURL := p.cloudParentURL() + "/sessions/" + url.PathEscape(sessionID)
	var cloudSess struct {
		Name         string         `json:"name"`
		UserID       string         `json:"userId"`
		UpdateTime   time.Time      `json:"updateTime"`
		SessionState map[string]any `json:"sessionState"`
	}
	code, err := p.doCloudJSON(ctx, http.MethodGet, sessURL, nil, &cloudSess)
	if err != nil {
		return sessionWireDTO{}, "", code, err
	}
	if cloudSess.UserID != "" && cloudSess.UserID != userID {
		return sessionWireDTO{}, "", http.StatusNotFound, errors.New("session belongs to a different user")
	}

	var allRawEvents []cloudSessionEventWire
	pageToken := ""
	for page := 0; page < maxCloudEventPages; page++ {
		eventsURL := sessURL + "/events"
		if pageToken != "" {
			eventsURL += "?pageToken=" + url.QueryEscape(pageToken)
		}
		var eventsResp struct {
			SessionEvents []cloudSessionEventWire `json:"sessionEvents"`
			NextPageToken string                  `json:"nextPageToken"`
		}
		if evCode, evErr := p.doCloudJSON(ctx, http.MethodGet, eventsURL, nil, &eventsResp); evErr != nil {
			if page == 0 && evCode == http.StatusNotFound {
				break
			}
			return sessionWireDTO{}, "", evCode, evErr
		}
		allRawEvents = append(allRawEvents, eventsResp.SessionEvents...)
		nextTok := strings.TrimSpace(eventsResp.NextPageToken)
		if nextTok == "" || nextTok == pageToken {
			break
		}
		pageToken = nextTok
	}

	events := make([]*session.Event, 0, len(allRawEvents))
	for _, ce := range allRawEvents {
		events = append(events, decodeCloudSessionEvent(ce))
	}
	slices.SortStableFunc(events, func(a, b *session.Event) int {
		return a.Timestamp.Compare(b.Timestamp)
	})
	updatedAt := cloudSess.UpdateTime
	if updatedAt.IsZero() && len(events) > 0 {
		updatedAt = events[len(events)-1].Timestamp
	}

	resolvedID := extractCloudSessionIDFromResource(cloudSess.Name, sessionID)
	return sessionWireDTO{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionID,
		State:     cloudSess.SessionState,
		Events:    events,
		UpdatedAt: updatedAt,
	}, resolvedID, http.StatusOK, nil
}

// maxCloudSessionListPages bounds pagination over ReasoningEngine /sessions
// (100 sessions per page -> up to 1,000 sessions) during session deletion so
// teardown latency stays bounded even on shared long-lived ReasoningEngines.
const maxCloudSessionListPages = 10

func stateStringField(state map[string]any, key string) string {
	if state == nil {
		return ""
	}
	v, _ := state[key].(string)
	return strings.TrimSpace(v)
}

func (p *agentEngineProxy) cloudDeleteSession(ctx context.Context, requestedSessionID, cloudSessionID, userID string) error {
	parentURL := p.cloudParentURL()
	deleted := make(map[string]bool, 4)

	deleteByID := func(id string) error {
		id = strings.TrimSpace(id)
		if id == "" || deleted[id] {
			return nil
		}
		deleted[id] = true
		_, err := p.doCloudJSON(ctx, http.MethodDelete, parentURL+"/sessions/"+url.PathEscape(id), nil, nil)
		return err
	}

	firstErr := deleteByID(cloudSessionID)
	if requestedSessionID != cloudSessionID {
		_ = deleteByID(requestedSessionID)
	}

	wantInc := inferIncidentID(requestedSessionID, nil)
	pageToken := ""
	for range maxCloudSessionListPages {
		listURL := parentURL + "/sessions"
		if pageToken != "" {
			listURL += "?pageToken=" + url.QueryEscape(pageToken)
		}
		var listResp struct {
			Sessions []struct {
				Name         string         `json:"name"`
				UserID       string         `json:"userId"`
				SessionState map[string]any `json:"sessionState"`
			} `json:"sessions"`
			NextPageToken string `json:"nextPageToken"`
		}
		if _, err := p.doCloudJSON(ctx, http.MethodGet, listURL, nil, &listResp); err != nil {
			break
		}
		for _, cs := range listResp.Sessions {
			if cs.UserID != "" && userID != "" && cs.UserID != userID {
				continue
			}
			csID := extractCloudSessionIDFromResource(cs.Name, "")
			if csID == "" || deleted[csID] {
				continue
			}
			wsID := stateStringField(cs.SessionState, sessionStateKeyWorkshopID)
			incID := stateStringField(cs.SessionState, sessionStateKeyIncidentID)

			match := strings.EqualFold(csID, requestedSessionID) ||
				strings.EqualFold(csID, cloudSessionID) ||
				(wsID != "" && strings.EqualFold(wsID, requestedSessionID)) ||
				(wantInc != "" && incID != "" && strings.EqualFold(incID, wantInc))

			// Legacy server-assigned numeric sessions created before sessionState stamping
			// have empty wsID/incID; resolve the session's primary incident from its opening
			// turn so sibling INC-403 replies mentioning INC-402 are not misclassified.
			if !match && wantInc != "" && wsID == "" && incID == "" {
				match = strings.EqualFold(p.cloudSessionPrimaryIncident(ctx, parentURL, csID), wantInc)
			}
			if match {
				_ = deleteByID(csID)
			}
		}
		nextTok := strings.TrimSpace(listResp.NextPageToken)
		if nextTok == "" || nextTok == pageToken {
			break
		}
		pageToken = nextTok
	}
	return firstErr
}

func (p *agentEngineProxy) cloudSessionPrimaryIncident(ctx context.Context, parentURL, cloudSessID string) string {
	var eventsResp struct {
		SessionEvents []cloudSessionEventWire `json:"sessionEvents"`
	}
	eventsURL := parentURL + "/sessions/" + url.PathEscape(cloudSessID) + "/events"
	if _, err := p.doCloudJSON(ctx, http.MethodGet, eventsURL, nil, &eventsResp); err != nil {
		return ""
	}
	events := make([]*session.Event, 0, len(eventsResp.SessionEvents))
	for _, ce := range eventsResp.SessionEvents {
		events = append(events, decodeCloudSessionEvent(ce))
	}
	slices.SortStableFunc(events, func(a, b *session.Event) int {
		return a.Timestamp.Compare(b.Timestamp)
	})
	for _, ev := range events {
		if id := inferIncidentID("", ev.Actions.StateDelta); id != "" {
			return id
		}
	}
	for _, ev := range events {
		isUser := strings.EqualFold(ev.Author, "user") || (ev.Content != nil && strings.EqualFold(ev.Content.Role, genai.RoleUser))
		if isUser {
			if id := inferIncidentIDFromEvent("", ev); id != "" {
				return id
			}
		}
	}
	for _, ev := range events {
		if id := inferIncidentIDFromEvent("", ev); id != "" {
			return id
		}
	}
	return ""
}

func (p *agentEngineProxy) cloudAppendEvent(ctx context.Context, sessionID string, ev *session.Event) (int, error) {
	appendURL := p.cloudParentURL() + "/sessions/" + url.PathEscape(sessionID) + ":appendEvent"
	rawBytes, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	var rawMap map[string]any
	_ = json.Unmarshal(rawBytes, &rawMap)

	payload := map[string]any{
		"author":       ev.Author,
		"invocationId": ev.InvocationID,
		"timestamp":    ev.Timestamp.UTC().Format(time.RFC3339Nano),
		"rawEvent":     rawMap,
	}
	if ev.Content != nil {
		payload["content"] = ev.Content
	}
	return p.doCloudJSON(ctx, http.MethodPost, appendURL, payload, nil)
}

func (p *agentEngineProxy) cloudGenerateMemories(ctx context.Context, userID string, events []*session.Event) error {
	var directEvents []map[string]any
	for _, ev := range events {
		if ev != nil && ev.Content != nil {
			directEvents = append(directEvents, map[string]any{"content": ev.Content})
		}
	}
	if len(directEvents) == 0 {
		return nil
	}
	genURL := p.cloudParentURL() + "/memories:generate"
	payload := map[string]any{
		"directContentsSource": map[string]any{
			"events": directEvents,
		},
		"scope": map[string]string{
			"user_id": userID,
		},
	}
	_, err := p.doCloudJSON(ctx, http.MethodPost, genURL, payload, nil)
	return err
}

func (p *agentEngineProxy) cloudRetrieveMemories(ctx context.Context, appName, userID, query string) ([]ConsolidatedMemoryFact, error) {
	retURL := p.cloudParentURL() + "/memories:retrieve"
	payload := map[string]any{
		"scope": map[string]string{
			"user_id": userID,
		},
	}
	if strings.TrimSpace(query) != "" {
		payload["similaritySearchParams"] = map[string]any{
			"searchQuery": query,
		}
	}
	var resp struct {
		RetrievedMemories []struct {
			Memory struct {
				Name       string    `json:"name"`
				Fact       string    `json:"fact"`
				UpdateTime time.Time `json:"updateTime"`
			} `json:"memory"`
		} `json:"retrievedMemories"`
	}
	if _, err := p.doCloudJSON(ctx, http.MethodPost, retURL, payload, &resp); err != nil {
		return nil, err
	}
	out := make([]ConsolidatedMemoryFact, 0, len(resp.RetrievedMemories))
	for i, item := range resp.RetrievedMemories {
		factText := strings.TrimSpace(item.Memory.Fact)
		if factText == "" {
			continue
		}
		id := item.Memory.Name
		if idx := strings.LastIndexByte(id, '/'); idx >= 0 && idx+1 < len(id) {
			id = id[idx+1:]
		}
		if id == "" {
			id = fmt.Sprintf("cloud-mem-%d", i+1)
		}
		ts := item.Memory.UpdateTime
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		out = append(out, buildConsolidatedFact(id, factText, appName, userID, "managed-memory-bank", "", ts, []string{"managed-memory"}))
	}
	return out, nil
}

func (p *agentEngineProxy) doCloudJSON(ctx context.Context, method, targetURL string, reqBody any, respOut any) (int, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return 0, err
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, bodyReader)
	if err != nil {
		return 0, err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.authedClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("upstream HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if respOut != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, respOut); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// reconstructToolConfirmationActions restores ADK EventActions.RequestedToolConfirmations
// and LongRunningToolIDs when OnePlatform ReasoningEngine SessionEvent storage strips
// unknown rawEvent fields and returns only genai.Content FunctionCall parts.
func reconstructToolConfirmationActions(ev *session.Event) {
	if ev == nil || ev.Content == nil {
		return
	}
	for _, part := range ev.Content.Parts {
		if part == nil || part.FunctionCall == nil {
			continue
		}
		fc := part.FunctionCall
		if fc.Name != toolconfirmation.FunctionCallName || strings.TrimSpace(fc.ID) == "" {
			continue
		}
		if ev.Actions.RequestedToolConfirmations == nil {
			ev.Actions.RequestedToolConfirmations = make(map[string]toolconfirmation.ToolConfirmation)
		}
		if _, exists := ev.Actions.RequestedToolConfirmations[fc.ID]; !exists {
			var tc toolconfirmation.ToolConfirmation
			if fc.Args != nil {
				if rawTC, ok := fc.Args["toolConfirmation"]; ok {
					if b, err := json.Marshal(rawTC); err == nil {
						_ = json.Unmarshal(b, &tc)
					}
				}
			}
			ev.Actions.RequestedToolConfirmations[fc.ID] = tc
		}
		if !slices.Contains(ev.LongRunningToolIDs, fc.ID) {
			ev.LongRunningToolIDs = append(ev.LongRunningToolIDs, fc.ID)
		}
	}
}
