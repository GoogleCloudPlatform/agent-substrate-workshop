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

package brokerclient_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/broker"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/brokerclient"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/opshub"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/sessiontestsuite"
	"google.golang.org/genai"
)

type headerRecordingTransport struct {
	base     http.RoundTripper
	mu       sync.Mutex
	authHdrs []string
}

func (t *headerRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.authHdrs = append(t.authHdrs, req.Header.Get("Authorization"))
	t.mu.Unlock()
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func newTestBrokerServer(t *testing.T, mod func(*broker.Config)) (*httptest.Server, *headerRecordingTransport) {
	t.Helper()
	ops, err := opshub.NewServer(opshub.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opshub.NewServer: %v", err)
	}
	opsTS := httptest.NewServer(ops.Handler())
	t.Cleanup(opsTS.Close)

	cfg := broker.Config{
		Namespace:       "workshop-301",
		Atespace:        "workshop-301",
		SimNamespace:    "prod-sim",
		ProjectID:       "demo-project",
		Region:          "us-central1",
		ModelLocation:   "global",
		GeminiModel:     "gemini-3.8-flash",
		MockLLM:         true,
		DefaultTemplate: "sre-agent-v2",
		OpsHubURL:       opsTS.URL,
		IdleAfter:       15 * time.Second,
	}
	if mod != nil {
		mod(&cfg)
	}
	srv, err := broker.NewServer(cfg)
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	rec := &headerRecordingTransport{base: ts.Client().Transport}
	return ts, rec
}

func TestSessionServiceConformanceSuite(t *testing.T) {
	opts := sessiontestsuite.SuiteOptions{
		SupportsUserProvidedSessionID: true,
		ProvidesServerAssignedEventID: false,
		AppName:                       "substrate-sre-triage",
	}
	sessiontestsuite.RunServiceTests(t, opts, func(t *testing.T) session.Service {
		ts, rec := newTestBrokerServer(t, nil)
		hc := &http.Client{Transport: rec, Timeout: 15 * time.Second}
		svc, err := brokerclient.NewSessionService(ts.URL, hc)
		if err != nil {
			t.Fatalf("NewSessionService: %v", err)
		}
		t.Cleanup(func() {
			rec.mu.Lock()
			hdrs := append([]string(nil), rec.authHdrs...)
			rec.mu.Unlock()
			for _, h := range hdrs {
				if h != "" {
					t.Errorf("keyless session client sent unexpected Authorization header %q", h)
				}
			}
		})
		return svc
	})
}

func TestConsolidatedMemoryExtractionAndZeroWakeLedgerAPIs(t *testing.T) {
	ts, rec := newTestBrokerServer(t, nil)
	hc := &http.Client{Transport: rec, Timeout: 15 * time.Second}

	sessSvc, err := brokerclient.NewSessionService(ts.URL, hc)
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}
	memSvc, err := brokerclient.NewMemoryService(ts.URL, hc)
	if err != nil {
		t.Fatalf("NewMemoryService: %v", err)
	}

	ctx := t.Context()
	created, err := sessSvc.Create(ctx, &session.CreateRequest{
		AppName:   "substrate-sre-triage",
		UserID:    "sre-operator",
		SessionID: "inc-402",
		State: map[string]any{
			"incident_id": "INC-402",
			"service":     "ledger-api",
		},
	})
	if err != nil {
		t.Fatalf("Create session inc-402: %v", err)
	}

	baseTS := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	events := []*session.Event{
		{
			ID:           "ev-prompt-1",
			Author:       "user",
			InvocationID: "inv-402-1",
			Timestamp:    baseTS,
			LLMResponse: model.LLMResponse{
				Content: genai.NewContentFromText("Investigate INC-402 connection pool exhaustion on ledger-api.", genai.RoleUser),
			},
		},
		{
			ID:           "ev-tool-call-1",
			Author:       "sre_triage_agent",
			InvocationID: "inv-402-1",
			Timestamp:    baseTS.Add(time.Second),
			LLMResponse: model.LLMResponse{
				Content: &genai.Content{
					Role: genai.RoleModel,
					Parts: []*genai.Part{
						genai.NewPartFromFunctionCall("read_workspace_file", map[string]any{"path": "ledger-api/config/pool.yaml"}),
					},
				},
			},
		},
		{
			ID:           "ev-tool-resp-1",
			Author:       "sre_triage_agent",
			InvocationID: "inv-402-1",
			Timestamp:    baseTS.Add(2 * time.Second),
			Actions: session.EventActions{
				StateDelta: map[string]any{"verified_config": "config/pool.yaml"},
			},
			LLMResponse: model.LLMResponse{
				Content: &genai.Content{
					Role: genai.RoleUser,
					Parts: []*genai.Part{
						genai.NewPartFromFunctionResponse("read_workspace_file", map[string]any{
							"path":    "ledger-api/config/pool.yaml",
							"content": "max_open_conns: 2\nidle_timeout_seconds: 0\nacquire_timeout_ms: 250\n",
						}),
					},
				},
			},
		},
		{
			ID:           "ev-reply-1",
			Author:       "sre_triage_agent",
			InvocationID: "inv-402-1",
			Timestamp:    baseTS.Add(3 * time.Second),
			LLMResponse: model.LLMResponse{
				Content: genai.NewContentFromText(
					"Verified INC-402 root cause in ledger-api: MAX_OPEN_CONNS=2 (max_open_conns: 2) with IDLE_TIMEOUT_SECONDS=0 in config/pool.yaml and unclosed rows cursor before ValidateChecksum in ReconcileTransfer (pool/store.go). Tune max_open_conns to 32 and idle_timeout_seconds to 30 and add defer rows.Close().",
					genai.RoleModel,
				),
			},
		},
	}
	for _, ev := range events {
		if err := sessSvc.AppendEvent(ctx, created.Session, ev); err != nil {
			t.Fatalf("AppendEvent(%s): %v", ev.ID, err)
		}
	}

	if err := memSvc.AddSessionToMemory(ctx, created.Session); err != nil {
		t.Fatalf("AddSessionToMemory: %v", err)
	}

	// Sibling query from INC-403 recalls distilled INC-402 pool configuration & cursor leak facts.
	searchOut, err := memSvc.SearchMemory(ctx, &memory.SearchRequest{
		AppName: "substrate-sre-triage",
		UserID:  "sre-operator",
		Query:   "INC-403 ledger-api 503 connection pool saturation MAX_OPEN_CONNS",
	})
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(searchOut.Memories) == 0 {
		t.Fatalf("SearchMemory returned 0 memories, want extracted INC-402 root-cause facts")
	}
	var combined strings.Builder
	for _, m := range searchOut.Memories {
		if m.Content != nil {
			for _, p := range m.Content.Parts {
				if p != nil {
					combined.WriteString(p.Text + "\n")
				}
			}
		}
	}
	joined := combined.String()
	for _, wantSub := range []string{"INC-402", "MAX_OPEN_CONNS=2", "IDLE_TIMEOUT_SECONDS=0", "rows.Close"} {
		if !strings.Contains(joined, wantSub) {
			t.Errorf("recalled memories missing %q; got:\n%s", wantSub, joined)
		}
	}

	// Verify zero-wake operator APIs: /api/ledger/sessions, /api/ledger/sessions/inc-402, /api/ledger/memories.
	listResp, err := http.Get(ts.URL + "/api/ledger/sessions")
	if err != nil {
		t.Fatalf("GET /api/ledger/sessions: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/ledger/sessions status = %d, want 200", listResp.StatusCode)
	}
	var listBody struct {
		Sessions []struct {
			SessionID  string `json:"session_id"`
			EventCount int    `json:"event_count"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode /api/ledger/sessions: %v", err)
	}
	if len(listBody.Sessions) != 1 || listBody.Sessions[0].SessionID != "inc-402" || listBody.Sessions[0].EventCount != 4 {
		t.Errorf("/api/ledger/sessions = %+v, want [inc-402 with 4 events]", listBody.Sessions)
	}

	detailResp, err := http.Get(ts.URL + "/api/ledger/sessions/inc-402")
	if err != nil {
		t.Fatalf("GET /api/ledger/sessions/inc-402: %v", err)
	}
	defer detailResp.Body.Close()
	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/ledger/sessions/inc-402 status = %d, want 200", detailResp.StatusCode)
	}
	var detailBody struct {
		SessionID string         `json:"session_id"`
		State     map[string]any `json:"state"`
		Events    []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"events"`
	}
	if err := json.NewDecoder(detailResp.Body).Decode(&detailBody); err != nil {
		t.Fatalf("decode session detail: %v", err)
	}
	if detailBody.SessionID != "inc-402" || len(detailBody.Events) != 4 || detailBody.State["verified_config"] != "config/pool.yaml" {
		t.Errorf("session detail = %+v, want 4 events and verified_config state", detailBody)
	}

	memResp, err := http.Get(ts.URL + "/api/ledger/memories?q=MAX_OPEN_CONNS")
	if err != nil {
		t.Fatalf("GET /api/ledger/memories: %v", err)
	}
	defer memResp.Body.Close()
	if memResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/ledger/memories status = %d, want 200", memResp.StatusCode)
	}
	memRaw, _ := io.ReadAll(memResp.Body)
	if !strings.Contains(string(memRaw), "MAX_OPEN_CONNS=2") || !strings.Contains(string(memRaw), "inc-402") {
		t.Errorf("/api/ledger/memories?q=MAX_OPEN_CONNS = %s, want MAX_OPEN_CONNS=2 from inc-402", string(memRaw))
	}
}

func TestManagedAgentPlatformUpstreamDelegation(t *testing.T) {
	var (
		mu            sync.Mutex
		upstreamPaths []string
	)
	fakePlatform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		upstreamPaths = append(upstreamPaths, r.Method+" "+r.URL.Path)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		const base = "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/987654321"
		switch {
		case r.Method == http.MethodPost && r.URL.Path == base+"/sessions":
			sid := r.URL.Query().Get("sessionId")
			if sid == "" {
				sid = "auto-1"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": base + "/sessions/" + sid + "/operations/op-1",
				"done": true,
				"response": map[string]any{
					"name":       base + "/sessions/" + sid,
					"userId":     "sre-operator",
					"updateTime": "2026-11-14T09:00:00Z",
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == base+"/sessions/inc-402":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":         base + "/sessions/inc-402",
				"userId":       "sre-operator",
				"updateTime":   "2026-11-14T09:00:01Z",
				"sessionState": map[string]any{"incident_id": "INC-402"},
			})
		case r.Method == http.MethodGet && r.URL.Path == base+"/sessions/inc-402/events":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionEvents": []map[string]any{
					{
						"name":         base + "/sessions/inc-402/events/ev-cloud-1",
						"author":       "user",
						"invocationId": "inv-cloud-1",
						"timestamp":    "2026-11-14T09:00:01Z",
						"rawEvent": map[string]any{
							"id":           "ev-cloud-1",
							"author":       "user",
							"invocationId": "inv-cloud-1",
							"timestamp":    "2026-11-14T09:00:01Z",
							"content": map[string]any{
								"role":  "user",
								"parts": []map[string]any{{"text": "Cloud persisted turn"}},
							},
						},
					},
				},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":appendEvent"):
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == base+"/memories:generate":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": base + "/operations/mem-op-1", "done": true})
		case r.Method == http.MethodPost && r.URL.Path == base+"/memories:retrieve":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"retrievedMemories": []map[string]any{
					{
						"memory": map[string]any{
							"name":       base + "/memories/m-cloud-402",
							"fact":       "Managed Memory Bank fact: INC-402 caused by MAX_OPEN_CONNS=2 and IDLE_TIMEOUT_SECONDS=0 in ledger-api.",
							"updateTime": "2026-11-14T09:00:05Z",
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakePlatform.Close)

	ts, _ := newTestBrokerServer(t, func(cfg *broker.Config) {
		cfg.MockLLM = false
		cfg.GenAIUpstreamURL = fakePlatform.URL
		cfg.ReasoningEngineID = "987654321"
		cfg.AgentEngineUpstreamURL = fakePlatform.URL
	})

	sessSvc, err := brokerclient.NewSessionService(ts.URL, ts.Client())
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}
	memSvc, err := brokerclient.NewMemoryService(ts.URL, ts.Client())
	if err != nil {
		t.Fatalf("NewMemoryService: %v", err)
	}

	ctx := t.Context()
	created, err := sessSvc.Create(ctx, &session.CreateRequest{
		AppName:   "substrate-sre-triage",
		UserID:    "sre-operator",
		SessionID: "inc-402",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := sessSvc.AppendEvent(ctx, created.Session, &session.Event{
		ID:           "ev-cloud-1",
		Author:       "user",
		InvocationID: "inv-cloud-1",
		Timestamp:    time.Date(2026, 11, 14, 9, 0, 1, 0, time.UTC),
		LLMResponse:  model.LLMResponse{Content: genai.NewContentFromText("Cloud persisted turn", genai.RoleUser)},
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	gotSess, err := sessSvc.Get(ctx, &session.GetRequest{
		AppName:   "substrate-sre-triage",
		UserID:    "sre-operator",
		SessionID: "inc-402",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotSess.Session.Events().Len() != 1 {
		t.Errorf("cloud Get events len = %d, want 1", gotSess.Session.Events().Len())
	}

	if err := memSvc.AddSessionToMemory(ctx, gotSess.Session); err != nil {
		t.Fatalf("AddSessionToMemory: %v", err)
	}
	memSearch, err := memSvc.SearchMemory(ctx, &memory.SearchRequest{
		AppName: "substrate-sre-triage",
		UserID:  "sre-operator",
		Query:   "INC-402 MAX_OPEN_CONNS",
	})
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if len(memSearch.Memories) == 0 || !strings.Contains(memSearch.Memories[0].Content.Parts[0].Text, "Managed Memory Bank fact") {
		t.Errorf("SearchMemory = %+v, want Managed Memory Bank fact", memSearch.Memories)
	}

	mu.Lock()
	paths := strings.Join(upstreamPaths, "\n")
	mu.Unlock()
	for _, wantPath := range []string{
		"POST /v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/987654321/sessions",
		":appendEvent",
		"/memories:generate",
		"/memories:retrieve",
	} {
		if !strings.Contains(paths, wantPath) {
			t.Errorf("expected upstream delegation call %q, got:\n%s", wantPath, paths)
		}
	}
}
