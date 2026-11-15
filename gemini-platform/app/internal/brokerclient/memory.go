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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/telemetry"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// NewMemoryService constructs an ADK Go v2 memory.Service that extracts and
// queries Consolidated Memory through the Governed Broker's keyless
// /agentengine/memories routes.
func NewMemoryService(brokerURL string, hc *http.Client) (memory.Service, error) {
	base := strings.TrimRight(strings.TrimSpace(brokerURL), "/")
	if base == "" {
		return nil, errors.New("brokerclient: BROKER_URL is required")
	}
	hc = telemetry.WrapHTTPClient(hc, 30*time.Second)
	return &memoryServiceClient{
		baseURL: base,
		hc:      hc,
	}, nil
}

// NewMemoryConsolidationPlugin builds an ADK Go v2 Plugin whose AfterRunCallback
// reloads the persisted session and submits completed turns to Consolidated Memory.
func NewMemoryConsolidationPlugin(sessionSvc session.Service, memorySvc memory.Service) (*plugin.Plugin, error) {
	return plugin.New(plugin.Config{
		Name: "consolidated_memory_sync",
		AfterRunCallback: func(ic agent.InvocationContext) {
			if ic == nil || ic.Session() == nil || memorySvc == nil {
				return
			}
			sess := ic.Session()
			if sessionSvc != nil {
				if fresh, err := sessionSvc.Get(ic, &session.GetRequest{
					AppName:   sess.AppName(),
					UserID:    sess.UserID(),
					SessionID: sess.ID(),
				}); err == nil && fresh != nil && fresh.Session != nil {
					sess = fresh.Session
				}
			}
			_ = memorySvc.AddSessionToMemory(ic, sess)
		},
	})
}

type memoryServiceClient struct {
	baseURL string
	hc      *http.Client
}

type addSessionMemoryWireReq struct {
	AppName   string           `json:"app_name"`
	UserID    string           `json:"user_id"`
	SessionID string           `json:"session_id"`
	State     map[string]any   `json:"state,omitempty"`
	Events    []*session.Event `json:"events"`
	UpdatedAt time.Time        `json:"updated_at"`
}

type searchMemoryWireReq struct {
	AppName string `json:"app_name"`
	UserID  string `json:"user_id"`
	Query   string `json:"query"`
}

type memoryWireEntry struct {
	ID             string         `json:"id"`
	Fact           string         `json:"fact,omitempty"`
	Content        *genai.Content `json:"content,omitempty"`
	Author         string         `json:"author,omitempty"`
	SourceSession  string         `json:"source_session_id,omitempty"`
	IncidentID     string         `json:"incident_id,omitempty"`
	Timestamp      time.Time      `json:"timestamp"`
	Topics         []string       `json:"topics,omitempty"`
	CustomMetadata map[string]any `json:"custom_metadata,omitempty"`
}

func (c *memoryServiceClient) AddSessionToMemory(ctx context.Context, s session.Session) error {
	if s == nil {
		return errors.New("brokerclient: session is nil")
	}
	stateCopy := make(map[string]any)
	if st := s.State(); st != nil {
		for k, v := range st.All() {
			if !strings.HasPrefix(k, session.KeyPrefixTemp) {
				stateCopy[k] = v
			}
		}
	}
	var events []*session.Event
	if evs := s.Events(); evs != nil {
		for ev := range evs.All() {
			if ev != nil {
				events = append(events, ev)
			}
		}
	}
	return c.postJSON(ctx, "/agentengine/memories:addSession", addSessionMemoryWireReq{
		AppName:   s.AppName(),
		UserID:    s.UserID(),
		SessionID: s.ID(),
		State:     stateCopy,
		Events:    events,
		UpdatedAt: s.LastUpdateTime(),
	}, nil)
}

func (c *memoryServiceClient) SearchMemory(ctx context.Context, req *memory.SearchRequest) (*memory.SearchResponse, error) {
	if req == nil {
		return &memory.SearchResponse{Memories: []memory.Entry{}}, nil
	}
	var wireOut struct {
		Memories []memoryWireEntry `json:"memories"`
	}
	if err := c.postJSON(ctx, "/agentengine/memories:search", searchMemoryWireReq{
		AppName: req.AppName,
		UserID:  req.UserID,
		Query:   req.Query,
	}, &wireOut); err != nil {
		return nil, err
	}
	entries := make([]memory.Entry, 0, len(wireOut.Memories))
	for _, item := range wireOut.Memories {
		content := item.Content
		if content == nil && strings.TrimSpace(item.Fact) != "" {
			content = genai.NewContentFromText(item.Fact, genai.RoleUser)
		}
		meta := item.CustomMetadata
		if meta == nil && (item.SourceSession != "" || item.IncidentID != "") {
			meta = map[string]any{}
			if item.SourceSession != "" {
				meta["source_session_id"] = item.SourceSession
			}
			if item.IncidentID != "" {
				meta["incident_id"] = item.IncidentID
			}
		}
		entries = append(entries, memory.Entry{
			ID:             item.ID,
			Content:        content,
			Author:         item.Author,
			Timestamp:      item.Timestamp,
			CustomMetadata: meta,
		})
	}
	return &memory.SearchResponse{
		Memories: entries,
	}, nil
}

func (c *memoryServiceClient) postJSON(ctx context.Context, path string, reqBody any, respOut any) error {
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("brokerclient: encode %s: %w", path, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("brokerclient: build %s request: %w", path, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return fmt.Errorf("brokerclient: call %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("brokerclient %s HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if respOut != nil && len(body) > 0 {
		if err := json.Unmarshal(body, respOut); err != nil {
			return fmt.Errorf("brokerclient: decode %s: %w", path, err)
		}
	}
	return nil
}

var _ memory.Service = (*memoryServiceClient)(nil)
