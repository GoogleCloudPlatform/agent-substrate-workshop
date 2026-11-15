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

package brokerclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/telemetry"
	"google.golang.org/adk/v2/session"
)

// NewSessionService constructs an ADK Go v2 session.Service that persists
// Session Ledger state and turn events through the Governed Broker's keyless
// /agentengine/sessions routes.
func NewSessionService(brokerURL string, hc *http.Client) (session.Service, error) {
	base := strings.TrimRight(strings.TrimSpace(brokerURL), "/")
	if base == "" {
		return nil, errors.New("brokerclient: BROKER_URL is required")
	}
	hc = telemetry.WrapHTTPClient(hc, 30*time.Second)
	return &sessionServiceClient{
		baseURL: base,
		hc:      hc,
	}, nil
}

type sessionServiceClient struct {
	baseURL string
	hc      *http.Client
}

type sessionWireRecord struct {
	AppName   string           `json:"app_name"`
	UserID    string           `json:"user_id"`
	SessionID string           `json:"session_id"`
	State     map[string]any   `json:"state,omitempty"`
	Events    []*session.Event `json:"events,omitempty"`
	UpdatedAt time.Time        `json:"updated_at"`
}

type createSessionWireReq struct {
	AppName   string         `json:"app_name"`
	UserID    string         `json:"user_id"`
	SessionID string         `json:"session_id,omitempty"`
	State     map[string]any `json:"state,omitempty"`
}

type getSessionWireReq struct {
	AppName         string    `json:"app_name"`
	UserID          string    `json:"user_id"`
	SessionID       string    `json:"session_id"`
	NumRecentEvents int       `json:"num_recent_events,omitempty"`
	After           time.Time `json:"after,omitempty"`
}

type listSessionsWireReq struct {
	AppName string `json:"app_name"`
	UserID  string `json:"user_id,omitempty"`
}

type deleteSessionWireReq struct {
	AppName   string `json:"app_name"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
}

type appendEventWireReq struct {
	AppName   string         `json:"app_name"`
	UserID    string         `json:"user_id"`
	SessionID string         `json:"session_id"`
	Event     *session.Event `json:"event"`
}

func (c *sessionServiceClient) Create(ctx context.Context, req *session.CreateRequest) (*session.CreateResponse, error) {
	if req == nil || strings.TrimSpace(req.AppName) == "" || strings.TrimSpace(req.UserID) == "" {
		return nil, errors.New("brokerclient: app_name and user_id are required")
	}
	var wire sessionWireRecord
	statusCode, err := c.postJSON(ctx, "/agentengine/sessions:create", createSessionWireReq{
		AppName:   req.AppName,
		UserID:    req.UserID,
		SessionID: req.SessionID,
		State:     req.State,
	}, &wire)
	if err != nil {
		return nil, mapSessionHTTPError(statusCode, req.SessionID, err)
	}
	return &session.CreateResponse{
		Session: newBrokerSession(wire),
	}, nil
}

func (c *sessionServiceClient) Get(ctx context.Context, req *session.GetRequest) (*session.GetResponse, error) {
	if req == nil || strings.TrimSpace(req.AppName) == "" || strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.SessionID) == "" {
		return nil, errors.New("brokerclient: app_name, user_id, and session_id are required")
	}
	var wire sessionWireRecord
	statusCode, err := c.postJSON(ctx, "/agentengine/sessions:get", getSessionWireReq{
		AppName:         req.AppName,
		UserID:          req.UserID,
		SessionID:       req.SessionID,
		NumRecentEvents: req.NumRecentEvents,
		After:           req.After,
	}, &wire)
	if err != nil {
		return nil, mapSessionHTTPError(statusCode, req.SessionID, err)
	}
	return &session.GetResponse{
		Session: newBrokerSession(wire),
	}, nil
}

func (c *sessionServiceClient) List(ctx context.Context, req *session.ListRequest) (*session.ListResponse, error) {
	if req == nil || strings.TrimSpace(req.AppName) == "" {
		return nil, errors.New("brokerclient: app_name is required")
	}
	var out struct {
		Sessions []sessionWireRecord `json:"sessions"`
	}
	statusCode, err := c.postJSON(ctx, "/agentengine/sessions:list", listSessionsWireReq{
		AppName: req.AppName,
		UserID:  req.UserID,
	}, &out)
	if err != nil {
		return nil, mapSessionHTTPError(statusCode, "", err)
	}
	list := make([]session.Session, 0, len(out.Sessions))
	for _, item := range out.Sessions {
		list = append(list, newBrokerSession(item))
	}
	return &session.ListResponse{
		Sessions: list,
	}, nil
}

func (c *sessionServiceClient) Delete(ctx context.Context, req *session.DeleteRequest) error {
	if req == nil || strings.TrimSpace(req.AppName) == "" || strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.SessionID) == "" {
		return errors.New("brokerclient: app_name, user_id, and session_id are required")
	}
	statusCode, err := c.postJSON(ctx, "/agentengine/sessions:delete", deleteSessionWireReq{
		AppName:   req.AppName,
		UserID:    req.UserID,
		SessionID: req.SessionID,
	}, nil)
	if err != nil {
		return mapSessionHTTPError(statusCode, req.SessionID, err)
	}
	return nil
}

func (c *sessionServiceClient) AppendEvent(ctx context.Context, curSession session.Session, event *session.Event) error {
	if curSession == nil || strings.TrimSpace(curSession.ID()) == "" {
		return errors.New("brokerclient: session is required")
	}
	if event == nil {
		return errors.New("brokerclient: event is nil")
	}
	if event.Partial {
		return nil
	}
	if event.ID == "" {
		event.ID = newEventID()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	persisted := trimTempDelta(event)
	statusCode, err := c.postJSON(ctx, "/agentengine/sessions:appendEvent", appendEventWireReq{
		AppName:   curSession.AppName(),
		UserID:    curSession.UserID(),
		SessionID: curSession.ID(),
		Event:     persisted,
	}, nil)
	if err != nil {
		return mapSessionHTTPError(statusCode, curSession.ID(), err)
	}

	if bs, ok := curSession.(*brokerSession); ok {
		bs.applyLocalAppend(event, persisted)
	}
	return nil
}

func (c *sessionServiceClient) postJSON(ctx context.Context, path string, reqBody any, respOut any) (int, error) {
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return 0, fmt.Errorf("brokerclient: encode %s: %w", path, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return 0, fmt.Errorf("brokerclient: build %s request: %w", path, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("brokerclient: call %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errDoc struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &errDoc)
		msg := strings.TrimSpace(errDoc.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if respOut != nil && len(body) > 0 {
		if err := json.Unmarshal(body, respOut); err != nil {
			return resp.StatusCode, fmt.Errorf("brokerclient: decode %s: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

func mapSessionHTTPError(statusCode int, sessionID string, err error) error {
	if statusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %q (%v)", session.ErrNotFound, sessionID, err)
	}
	return err
}

func trimTempDelta(event *session.Event) *session.Event {
	if len(event.Actions.StateDelta) == 0 {
		return event
	}
	filtered := make(map[string]any, len(event.Actions.StateDelta))
	for k, v := range event.Actions.StateDelta {
		if !strings.HasPrefix(k, session.KeyPrefixTemp) {
			filtered[k] = v
		}
	}
	if len(filtered) == len(event.Actions.StateDelta) {
		return event
	}
	cp := *event
	cp.Actions.StateDelta = filtered
	return &cp
}

func newEventID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("ev-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

type brokerSession struct {
	appName   string
	userID    string
	sessionID string

	mu        sync.RWMutex
	state     map[string]any
	events    []*session.Event
	updatedAt time.Time
}

func newBrokerSession(w sessionWireRecord) *brokerSession {
	st := maps.Clone(w.State)
	if st == nil {
		st = make(map[string]any)
	}
	evs := slices.Clone(w.Events)
	ts := w.UpdatedAt
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return &brokerSession{
		appName:   w.AppName,
		userID:    w.UserID,
		sessionID: w.SessionID,
		state:     st,
		events:    evs,
		updatedAt: ts,
	}
}

func (s *brokerSession) ID() string      { return s.sessionID }
func (s *brokerSession) AppName() string { return s.appName }
func (s *brokerSession) UserID() string  { return s.userID }

func (s *brokerSession) State() session.State {
	return &brokerState{
		mu:    &s.mu,
		state: s.state,
	}
}

func (s *brokerSession) Events() session.Events {
	s.mu.RLock()
	cp := slices.Clone(s.events)
	s.mu.RUnlock()
	return brokerEvents(cp)
}

func (s *brokerSession) LastUpdateTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updatedAt
}

func (s *brokerSession) applyLocalAppend(rawEvent, trimmedEvent *session.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		s.state = make(map[string]any)
	}
	if len(rawEvent.Actions.StateDelta) > 0 {
		maps.Copy(s.state, rawEvent.Actions.StateDelta)
	}
	s.events = append(s.events, trimmedEvent)
	s.updatedAt = rawEvent.Timestamp
}

type brokerState struct {
	mu    *sync.RWMutex
	state map[string]any
}

func (s *brokerState) Get(key string) (any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.state[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return v, nil
}

func (s *brokerState) Set(key string, val any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state[key] = val
	return nil
}

func (s *brokerState) All() iter.Seq2[string, any] {
	s.mu.RLock()
	cp := maps.Clone(s.state)
	s.mu.RUnlock()
	return func(yield func(string, any) bool) {
		for k, v := range cp {
			if !yield(k, v) {
				return
			}
		}
	}
}

type brokerEvents []*session.Event

func (e brokerEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, ev := range e {
			if !yield(ev) {
				return
			}
		}
	}
}

func (e brokerEvents) Len() int { return len(e) }

func (e brokerEvents) At(i int) *session.Event {
	if i < 0 || i >= len(e) {
		return nil
	}
	return e[i]
}

var _ session.Service = (*sessionServiceClient)(nil)
var _ session.Session = (*brokerSession)(nil)
