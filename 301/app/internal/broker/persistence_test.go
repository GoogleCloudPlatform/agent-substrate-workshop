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
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/agentruntime"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/broker"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/brokerclient"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

func TestBrokerStateFilePersistsSessionsMemoriesAndSkillsAcrossRollouts(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "broker-state.json")
	v2Dir := filepath.Join("..", "..", "..", "modules", "03-skill-registry", "skills", "ledger-pool-triage", "v2")
	v2Zip, v2SHA, err := broker.BuildDeterministicSkillZipFromDir(v2Dir)
	if err != nil {
		t.Fatalf("BuildDeterministicSkillZipFromDir(v2): %v", err)
	}

	newGateway := func(t *testing.T) (*broker.Server, *httptest.Server) {
		t.Helper()
		srv, err := broker.NewServer(broker.Config{
			Namespace:       "workshop-301",
			Atespace:        "workshop-301",
			SimNamespace:    "prod-sim",
			ProjectID:       "demo-project",
			Region:          "us-central1",
			MockLLM:         true,
			DefaultTemplate: "sre-agent-v3",
			StateFile:       stateFile,
		})
		if err != nil {
			t.Fatalf("broker.NewServer: %v", err)
		}
		ts := httptest.NewServer(srv.Handler())
		return srv, ts
	}

	// Pod generation 1: populate Session Ledger, Consolidated Memory, and Skill Registry v2.
	srv1, ts1 := newGateway(t)
	ctx := context.Background()

	sessSvc1, err := brokerclient.NewSessionService(ts1.URL, ts1.Client())
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}
	memSvc1, err := brokerclient.NewMemoryService(ts1.URL, ts1.Client())
	if err != nil {
		t.Fatalf("NewMemoryService: %v", err)
	}

	created, err := sessSvc1.Create(ctx, &session.CreateRequest{
		AppName:   "sre_triage_app",
		UserID:    "sre-operator",
		SessionID: "inc-402",
		State:     map[string]any{"incident_id": "INC-402"},
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	ev := session.NewEvent(ctx, "inv-persist-1")
	ev.Author = "sre_triage_agent"
	ev.Content = genai.NewContentFromText(
		"Verified INC-402 root cause: config/pool.yaml sets max_open_conns: 2 and idle_timeout_seconds: 0 while pool/store.go ReconcileTransfer misses defer rows.Close() before ValidateChecksum.",
		genai.RoleModel,
	)
	if err := sessSvc1.AppendEvent(ctx, created.Session, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	gotSess1, err := sessSvc1.Get(ctx, &session.GetRequest{
		AppName:   "sre_triage_app",
		UserID:    "sre-operator",
		SessionID: "inc-402",
	})
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	if err := memSvc1.AddSessionToMemory(ctx, gotSess1.Session); err != nil {
		t.Fatalf("AddSessionToMemory: %v", err)
	}

	uploadBody, _ := json.Marshal(map[string]any{
		"skill":          "ledger-pool-triage",
		"revision":       "v2",
		"sha256":         v2SHA,
		"archive_base64": base64.StdEncoding.EncodeToString(v2Zip),
		"set_default":    true,
	})
	upResp, err := ts1.Client().Post(ts1.URL+"/api/catalog/skills", "application/json", bytes.NewReader(uploadBody))
	if err != nil {
		t.Fatalf("POST /api/catalog/skills: %v", err)
	}
	defer upResp.Body.Close()
	if upResp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(upResp.Body)
		t.Fatalf("POST /api/catalog/skills status = %d: %s", upResp.StatusCode, string(raw))
	}
	var upSummary broker.CatalogSkillSummary
	if err := json.NewDecoder(upResp.Body).Decode(&upSummary); err != nil {
		t.Fatalf("decode skill summary: %v", err)
	}
	if !strings.Contains(upSummary.ResourceName, "/locations/global/skills/ledger-pool-triage") {
		t.Fatalf("Skill ResourceName = %q, want global location (/locations/global/skills/ledger-pool-triage)", upSummary.ResourceName)
	}

	// Shut down Pod generation 1 completely before starting Pod generation 2.
	ts1.Close()
	_ = srv1.Close()

	// Pod generation 2: verify Session Ledger, Consolidated Memory search, and Skill Registry v2 survived rollout.
	srv2, ts2 := newGateway(t)
	t.Cleanup(ts2.Close)
	t.Cleanup(func() { _ = srv2.Close() })

	ledgerResp, err := ts2.Client().Get(ts2.URL + "/api/ledger/sessions/inc-402")
	if err != nil {
		t.Fatalf("GET /api/ledger/sessions/inc-402 after rollout: %v", err)
	}
	defer ledgerResp.Body.Close()
	if ledgerResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/ledger/sessions/inc-402 status = %d, want 200", ledgerResp.StatusCode)
	}
	var detail broker.LedgerSessionDetail
	if err := json.NewDecoder(ledgerResp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode LedgerSessionDetail: %v", err)
	}
	if detail.EventCount < 1 {
		t.Fatalf("persisted EventCount = %d, want >= 1", detail.EventCount)
	}

	memSvc2, err := brokerclient.NewMemoryService(ts2.URL, ts2.Client())
	if err != nil {
		t.Fatalf("NewMemoryService(pod2): %v", err)
	}
	searchOut, err := memSvc2.SearchMemory(ctx, &memory.SearchRequest{
		AppName: "sre_triage_app",
		UserID:  "sre-operator",
		Query:   "MAX_OPEN_CONNS rows.Close",
	})
	if err != nil {
		t.Fatalf("SearchMemory after rollout: %v", err)
	}
	if len(searchOut.Memories) == 0 {
		t.Fatalf("expected Consolidated Memory facts to survive sre-gateway rollout, got 0")
	}

	catResp, err := ts2.Client().Get(ts2.URL + "/api/catalog/skills")
	if err != nil {
		t.Fatalf("GET /api/catalog/skills after rollout: %v", err)
	}
	defer catResp.Body.Close()
	var catOut struct {
		Skills []broker.CatalogSkillSummary `json:"skills"`
	}
	if err := json.NewDecoder(catResp.Body).Decode(&catOut); err != nil {
		t.Fatalf("decode catalog skills: %v", err)
	}
	if len(catOut.Skills) == 0 || catOut.Skills[0].DefaultRevision != "v2" || catOut.Skills[0].ActiveSHA256 != v2SHA {
		t.Fatalf("after rollout skills = %+v, want default_revision=v2 sha=%s", catOut.Skills, v2SHA)
	}

	dlResp, err := ts2.Client().Get(ts2.URL + "/agentregistry/skills/ledger-pool-triage/revisions/v2:download")
	if err != nil {
		t.Fatalf("download v2 zip after rollout: %v", err)
	}
	defer dlResp.Body.Close()
	gotZip, _ := io.ReadAll(dlResp.Body)
	if !bytes.Equal(gotZip, v2Zip) {
		t.Fatalf("downloaded v2 zip after rollout len=%d, want exact %d bytes", len(gotZip), len(v2Zip))
	}
}

func TestAgentEngineAutoBootstrapsReasoningEngineInCluster(t *testing.T) {
	var createdSessions atomic.Int64
	fakePlatform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines":
			_, _ = w.Write([]byte(`{"reasoningEngines":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines":
			_, _ = w.Write([]byte(`{"name":"projects/demo-project/locations/us-central1/operations/op-re-1","done":false}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/operations/op-re-1":
			_, _ = w.Write([]byte(`{"name":"projects/demo-project/locations/us-central1/operations/op-re-1","done":true,"response":{"name":"projects/demo-project/locations/us-central1/reasoningEngines/77889900"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/77889900/sessions":
			createdSessions.Add(1)
			_, _ = w.Write([]byte(`{"done":true,"response":{"name":"projects/demo-project/locations/us-central1/reasoningEngines/77889900/sessions/inc-402"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakePlatform.Close)

	stateFile := filepath.Join(t.TempDir(), "auto-re-state.json")
	srv, err := broker.NewServer(broker.Config{
		Namespace:              "workshop-301",
		Atespace:               "workshop-301",
		SimNamespace:           "prod-sim",
		ProjectID:              "demo-project",
		Region:                 "us-central1",
		MockLLM:                false,
		ReasoningEngineID:      "auto",
		AgentEngineUpstreamURL: fakePlatform.URL,
		GenAIUpstreamURL:       fakePlatform.URL,
		SkillRegistryUpstreamURL: fakePlatform.URL,
		AgentRegistryUpstreamURL: fakePlatform.URL,
		CloudTraceUpstreamURL:  fakePlatform.URL,
		StateFile:              stateFile,
	})
	if err != nil {
		t.Fatalf("broker.NewServer(REASONING_ENGINE_ID=auto): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	sessSvc, err := brokerclient.NewSessionService(ts.URL, ts.Client())
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = sessSvc.Create(context.Background(), &session.CreateRequest{
			AppName:   "sre_triage_app",
			UserID:    "sre-operator",
			SessionID: "probe-" + time.Now().Format("150405.000000"),
		})
		if createdSessions.Load() > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if createdSessions.Load() == 0 {
		t.Fatalf("expected REASONING_ENGINE_ID=auto to provision runtime 77889900 and delegate CreateSession")
	}
}

func TestCloudSessionPreservesToolConfirmationActions(t *testing.T) {
	// Real OnePlatform ReasoningEngine SessionEvents strip unknown rawEvent fields
	// and store only author/invocationId/timestamp/content. Verify sessions:get
	// preserves RequestedToolConfirmations and LongRunningToolIDs so ADK Go v2
	// executes confirmed apply_remediation_pr calls instead of re-prompting.
	var storedCloudEvents []map[string]any
	fakeCloudRE := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions":
			_, _ = w.Write([]byte(`{"done":true,"response":{"name":"projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-402"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-402:appendEvent":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			// Strip rawEvent just like OnePlatform SessionEvent proto unmarshaling.
			storedCloudEvents = append(storedCloudEvents, map[string]any{
				"name":         "projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-402/events/ev-1",
				"author":       body["author"],
				"invocationId": body["invocationId"],
				"timestamp":    body["timestamp"],
				"content":      body["content"],
			})
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-402":
			_, _ = w.Write([]byte(`{"name":"projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-402","userId":"sre-operator"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-402/events":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessionEvents": storedCloudEvents})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakeCloudRE.Close)

	newServer := func(stateFile string) *httptest.Server {
		srv, err := broker.NewServer(broker.Config{
			Namespace:                "workshop-301",
			Atespace:                 "workshop-301",
			SimNamespace:             "prod-sim",
			ProjectID:                "demo-project",
			Region:                   "us-central1",
			MockLLM:                  false,
			ReasoningEngineID:        "556677",
			AgentEngineUpstreamURL:   fakeCloudRE.URL,
			GenAIUpstreamURL:         fakeCloudRE.URL,
			SkillRegistryUpstreamURL: fakeCloudRE.URL,
			AgentRegistryUpstreamURL: fakeCloudRE.URL,
			CloudTraceUpstreamURL:    fakeCloudRE.URL,
			StateFile:                stateFile,
		})
		if err != nil {
			t.Fatalf("broker.NewServer: %v", err)
		}
		t.Cleanup(func() { _ = srv.Close() })
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(ts.Close)
		return ts
	}

	ts1 := newServer(filepath.Join(t.TempDir(), "state1.json"))
	ctx := context.Background()
	sessSvc1, err := brokerclient.NewSessionService(ts1.URL, ts1.Client())
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}
	created, err := sessSvc1.Create(ctx, &session.CreateRequest{
		AppName:   "sre_triage_app",
		UserID:    "sre-operator",
		SessionID: "inc-402",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ev := session.NewEvent(ctx, "inv-confirm")
	ev.Author = "sre_triage_agent"
	fc := &genai.FunctionCall{
		ID:   "adk-confirm-call-1",
		Name: toolconfirmation.FunctionCallName,
		Args: map[string]any{
			"originalFunctionCall": map[string]any{
				"id":   "orig-call-1",
				"name": "apply_remediation_pr",
				"args": map[string]any{"incident_id": "INC-402", "branch": "fix/inc-402-pool-leak"},
			},
			"toolConfirmation": map[string]any{
				"hint":      "Approve or reject apply_remediation_pr",
				"confirmed": false,
			},
		},
	}
	ev.Content = &genai.Content{
		Role:  genai.RoleModel,
		Parts: []*genai.Part{{FunctionCall: fc}},
	}
	ev.LongRunningToolIDs = []string{"adk-confirm-call-1"}
	ev.Actions.RequestedToolConfirmations = map[string]toolconfirmation.ToolConfirmation{
		"adk-confirm-call-1": {Hint: "Approve or reject apply_remediation_pr"},
	}
	if err := sessSvc1.AppendEvent(ctx, created.Session, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	// Verify both same-broker Get AND fresh-broker cloud-only Get preserve RequestedToolConfirmations.
	for label, ts := range map[string]*httptest.Server{
		"same-broker":  ts1,
		"cloud-only":   newServer(filepath.Join(t.TempDir(), "empty-state.json")),
	} {
		svc, err := brokerclient.NewSessionService(ts.URL, ts.Client())
		if err != nil {
			t.Fatalf("%s NewSessionService: %v", label, err)
		}
		got, err := svc.Get(ctx, &session.GetRequest{
			AppName:   "sre_triage_app",
			UserID:    "sre-operator",
			SessionID: "inc-402",
		})
		if err != nil {
			t.Fatalf("%s Get: %v", label, err)
		}
		if got.Session.Events().Len() != 1 {
			t.Fatalf("%s expected 1 event, got %d", label, got.Session.Events().Len())
		}
		gotEv := got.Session.Events().At(0)
		if _, ok := gotEv.Actions.RequestedToolConfirmations["adk-confirm-call-1"]; !ok {
			t.Fatalf("%s lost Actions.RequestedToolConfirmations: %+v", label, gotEv.Actions)
		}
	}
}

func TestCloudSessionServerAssignedNumericIDMapsToWorkshopSessionID(t *testing.T) {
	// Managed Gemini Sessions ignores ?sessionId=inc-402 on CreateSession and returns
	// a server-generated numeric session ID (e.g., "9988776655"), returning 404 if
	// subsequent Get/AppendEvent/Delete calls pass "inc-402" instead of "9988776655".
	var appendedToNumeric atomic.Int64
	fakeNumericCloudRE := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions":
			_, _ = w.Write([]byte(`{"done":true,"response":{"name":"projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/9988776655"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/9988776655:appendEvent":
			appendedToNumeric.Add(1)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/9988776655":
			_, _ = w.Write([]byte(`{"name":"projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/9988776655","userId":"sre-operator"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/9988776655/events":
			_, _ = w.Write([]byte(`{"sessionEvents":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakeNumericCloudRE.Close)

	srv, err := broker.NewServer(broker.Config{
		Namespace:                "workshop-301",
		Atespace:                 "workshop-301",
		SimNamespace:             "prod-sim",
		ProjectID:                "demo-project",
		Region:                   "us-central1",
		MockLLM:                  false,
		ReasoningEngineID:        "556677",
		AgentEngineUpstreamURL:   fakeNumericCloudRE.URL,
		GenAIUpstreamURL:         fakeNumericCloudRE.URL,
		SkillRegistryUpstreamURL: fakeNumericCloudRE.URL,
		AgentRegistryUpstreamURL: fakeNumericCloudRE.URL,
		CloudTraceUpstreamURL:    fakeNumericCloudRE.URL,
		StateFile:                filepath.Join(t.TempDir(), "numeric-state.json"),
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx := context.Background()
	sessSvc, err := brokerclient.NewSessionService(ts.URL, ts.Client())
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}
	created, err := sessSvc.Create(ctx, &session.CreateRequest{
		AppName:   "sre_triage_app",
		UserID:    "sre-operator",
		SessionID: "inc-402",
	})
	if err != nil {
		t.Fatalf("Create(inc-402): %v", err)
	}
	if created.Session.ID() != "inc-402" {
		t.Fatalf("expected logical session ID inc-402 to be preserved, got %q", created.Session.ID())
	}

	// ADK AutoCreateSession callers address the session by the requested ID ("inc-402").
	got, err := sessSvc.Get(ctx, &session.GetRequest{
		AppName:   "sre_triage_app",
		UserID:    "sre-operator",
		SessionID: "inc-402",
	})
	if err != nil {
		t.Fatalf("Get(inc-402): %v", err)
	}
	ev := session.NewEvent(ctx, "inv-num-1")
	ev.Author = "sre_triage_agent"
	ev.Content = genai.NewContentFromText("Root cause: max_open_conns: 2", genai.RoleModel)
	if err := sessSvc.AppendEvent(ctx, got.Session, ev); err != nil {
		t.Fatalf("AppendEvent(inc-402): %v", err)
	}
	if appendedToNumeric.Load() != 1 {
		t.Fatalf("expected AppendEvent to route to upstream numeric session 9988776655, got %d calls", appendedToNumeric.Load())
	}
}

func TestSkillRegistryCloudSyncNormalizesPrivateSkillAndRevPrefix(t *testing.T) {
	v2Dir := filepath.Join("..", "..", "..", "modules", "03-skill-registry", "skills", "ledger-pool-triage", "v2")
	v2Zip, v2SHA, err := broker.BuildDeterministicSkillZipFromDir(v2Dir)
	if err != nil {
		t.Fatalf("BuildDeterministicSkillZipFromDir(v2): %v", err)
	}

	var patchedDefaultRev atomic.Value
	fakeCloudAR := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/demo-project/locations/global/skills":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"skills":[{"name":"projects/demo-project/locations/global/skills/private-ledger-pool-triage","displayName":"ledger-pool-triage","defaultRevision":"projects/demo-project/locations/global/skills/private-ledger-pool-triage/revisions/rev-v2","state":"STATE_ACTIVE"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/demo-project/locations/global/skills/private-ledger-pool-triage":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"projects/demo-project/locations/global/skills/private-ledger-pool-triage","displayName":"ledger-pool-triage","defaultRevision":"projects/demo-project/locations/global/skills/private-ledger-pool-triage/revisions/rev-v2","state":"STATE_ACTIVE"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/demo-project/locations/global/skills/private-ledger-pool-triage/revisions/rev-v2":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"projects/demo-project/locations/global/skills/private-ledger-pool-triage/revisions/rev-v2","sha256Hash":"` + v2SHA + `","state":"STATE_ACTIVE"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/download/v1/projects/demo-project/locations/global/skills/private-ledger-pool-triage/revisions/rev-v2" && r.URL.Query().Get("alt") == "media":
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(v2Zip)
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/projects/demo-project/locations/global/skills/private-ledger-pool-triage":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			patchedDefaultRev.Store(body["defaultRevision"])
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakeCloudAR.Close)

	srv, err := broker.NewServer(broker.Config{
		Namespace:                "workshop-301",
		Atespace:                 "workshop-301",
		SimNamespace:             "prod-sim",
		ProjectID:                "demo-project",
		Region:                   "us-central1",
		SkillRegistryLocation:    "global",
		MockLLM:                  false,
		GenAIUpstreamURL:         fakeCloudAR.URL,
		AgentEngineUpstreamURL:   fakeCloudAR.URL,
		SkillRegistryUpstreamURL: fakeCloudAR.URL,
		AgentRegistryUpstreamURL: fakeCloudAR.URL,
		CloudTraceUpstreamURL:    fakeCloudAR.URL,
		StateFile:                filepath.Join(t.TempDir(), "skill-cloud-sync.json"),
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Querying GET /api/catalog/skills?sync=true pulls private-ledger-pool-triage@rev-v2
	// from Cloud Skill Registry and exposes it under logical skill name ledger-pool-triage@v2.
	resp, err := ts.Client().Get(ts.URL + "/api/catalog/skills?sync=true")
	if err != nil {
		t.Fatalf("GET /api/catalog/skills?sync=true: %v", err)
	}
	defer resp.Body.Close()
	var cat struct {
		Skills []struct {
			Name            string `json:"name"`
			DefaultRevision string `json:"default_revision"`
			ActiveSHA256    string `json:"active_sha256"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cat); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if len(cat.Skills) != 1 || cat.Skills[0].Name != "ledger-pool-triage" || cat.Skills[0].DefaultRevision != "v2" || cat.Skills[0].ActiveSHA256 != v2SHA {
		t.Fatalf("expected cloud sync to normalize private-ledger-pool-triage@rev-v2 to ledger-pool-triage@v2 (%s), got %+v", v2SHA, cat.Skills)
	}

	// Promoting v1 via POST /api/catalog/skills patches
	// private-ledger-pool-triage with .../revisions/rev-v1 in Cloud Skill Registry.
	csrfReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/session", nil)
	csrfResp, err := ts.Client().Do(csrfReq)
	if err != nil {
		t.Fatalf("GET /api/session: %v", err)
	}
	_ = csrfResp.Body.Close()
	var csrfTok string
	var csrfCookie *http.Cookie
	for _, c := range csrfResp.Cookies() {
		if c.Name == "sre_csrf" {
			csrfTok = c.Value
			csrfCookie = c
		}
	}
	patchReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/catalog/skills", strings.NewReader(`{"skill":"ledger-pool-triage","default_revision":"v1"}`))
	patchReq.Header.Set("Content-Type", "application/json")
	if csrfCookie != nil {
		patchReq.AddCookie(csrfCookie)
		patchReq.Header.Set("X-CSRF-Token", csrfTok)
	}
	patchResp, err := ts.Client().Do(patchReq)
	if err != nil {
		t.Fatalf("POST default revision: %v", err)
	}
	_ = patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("POST default revision status = %d, want 200", patchResp.StatusCode)
	}
	wantPatch := "projects/demo-project/locations/global/skills/private-ledger-pool-triage/revisions/rev-v1"
	if gotPatch, _ := patchedDefaultRev.Load().(string); gotPatch != wantPatch {
		t.Fatalf("PATCH defaultRevision = %q, want %q", gotPatch, wantPatch)
	}
}

func TestPreExistingPaginatedCloudSessionHydratesLedgerAndPreservesConfirmation(t *testing.T) {
	type cloudEv struct {
		Name         string          `json:"name"`
		Author       string          `json:"author"`
		InvocationID string          `json:"invocationId"`
		Timestamp    time.Time       `json:"timestamp"`
		Content      *genai.Content  `json:"content,omitempty"`
		RawEvent     json.RawMessage `json:"rawEvent,omitempty"`
	}

	baseTS := time.Date(2026, 11, 15, 2, 0, 0, 0, time.UTC)
	cloudEvents := make([]cloudEv, 0, 106)
	for i := 0; i < 105; i++ {
		ts := baseTS.Add(time.Duration(i) * time.Second)
		cloudEvents = append(cloudEvents, cloudEv{
			Name:         "projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-402/events/" + time.Duration(i).String(),
			Author:       "sre_triage_agent",
			InvocationID: "inv-prior",
			Timestamp:    ts,
			Content:      genai.NewContentFromText("prior diagnostic turn", genai.RoleModel),
		})
	}

	fakePaginatedRE := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-402":
			_, _ = w.Write([]byte(`{"name":"projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-402","userId":"sre-operator","sessionState":{"incident_id":"INC-402"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-402/events":
			tok := r.URL.Query().Get("pageToken")
			if tok == "" {
				page1 := cloudEvents
				next := ""
				if len(page1) > 100 {
					page1 = cloudEvents[:100]
					next = "page-2"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"sessionEvents": page1,
					"nextPageToken": next,
				})
				return
			}
			if tok == "page-2" {
				var page2 []cloudEv
				if len(cloudEvents) > 100 {
					page2 = cloudEvents[100:]
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"sessionEvents": page2,
				})
				return
			}
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-402:appendEvent":
			var body struct {
				Author       string         `json:"author"`
				InvocationID string         `json:"invocationId"`
				Timestamp    time.Time      `json:"timestamp"`
				Content      *genai.Content `json:"content"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			// Simulate live ReasoningEngine stripping unknown rawEvent.Actions fields.
			cloudEvents = append(cloudEvents, cloudEv{
				Name:         "projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-402/events/ev-106",
				Author:       body.Author,
				InvocationID: body.InvocationID,
				Timestamp:    body.Timestamp,
				Content:      body.Content,
			})
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-403":
			_, _ = w.Write([]byte(`{"name":"projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-403","userId":"sre-operator"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-403/events":
			_, _ = w.Write([]byte(`{"sessionEvents":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-403:appendEvent":
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-truncated":
			_, _ = w.Write([]byte(`{"name":"projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-truncated","userId":"sre-operator"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/inc-truncated/events":
			if r.URL.Query().Get("pageToken") == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"sessionEvents": cloudEvents[:2],
					"nextPageToken": "page-error",
				})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"upstream pagination failure"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakePaginatedRE.Close)

	srv, err := broker.NewServer(broker.Config{
		Namespace:                "workshop-301",
		Atespace:                 "workshop-301",
		SimNamespace:             "prod-sim",
		ProjectID:                "demo-project",
		Region:                   "us-central1",
		MockLLM:                  false,
		ReasoningEngineID:        "556677",
		AgentEngineUpstreamURL:   fakePaginatedRE.URL,
		GenAIUpstreamURL:         fakePaginatedRE.URL,
		SkillRegistryUpstreamURL: fakePaginatedRE.URL,
		AgentRegistryUpstreamURL: fakePaginatedRE.URL,
		CloudTraceUpstreamURL:    fakePaginatedRE.URL,
		StateFile:                filepath.Join(t.TempDir(), "hydrated-state.json"),
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx := context.Background()
	sessSvc, err := brokerclient.NewSessionService(ts.URL, ts.Client())
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}

	// 1. Pre-existing paginated session inc-402 (>100 events) returns all 105 events and hydrates /api/ledger/sessions/inc-402.
	got1, err := sessSvc.Get(ctx, &session.GetRequest{
		AppName:   "sre_triage_app",
		UserID:    "sre-operator",
		SessionID: "inc-402",
	})
	if err != nil {
		t.Fatalf("Get(inc-402): %v", err)
	}
	if got1.Session.Events().Len() != 105 {
		t.Fatalf("Get(inc-402) events len = %d, want 105 across paginated responses", got1.Session.Events().Len())
	}

	ledgerResp, err := ts.Client().Get(ts.URL + "/api/ledger/sessions/inc-402")
	if err != nil {
		t.Fatalf("GET /api/ledger/sessions/inc-402: %v", err)
	}
	_ = ledgerResp.Body.Close()
	if ledgerResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/ledger/sessions/inc-402 status = %d, want 200 OK after cloud hydration", ledgerResp.StatusCode)
	}

	// 2. Appending event #106 with adk_request_confirmation preserves RequestedToolConfirmations on subsequent Get.
	confirmEv := session.NewEvent(ctx, "inv-gate-106")
	confirmEv.ID = "ev-106"
	confirmEv.Timestamp = baseTS.Add(200 * time.Second)
	confirmEv.Author = "sre_triage_agent"
	confirmEv.Content = &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			genai.NewPartFromFunctionCall(toolconfirmation.FunctionCallName, map[string]any{
				"originalFunctionCall": map[string]any{
					"id":   "orig-apply-106",
					"name": "apply_remediation_pr",
					"args": map[string]any{"incident_id": "INC-402", "branch": "fix/inc-402-pool-leak"},
				},
				"toolConfirmation": map[string]any{
					"hint":      "Approve Remediation Gate",
					"confirmed": false,
				},
			}),
		},
	}
	confirmEv.Content.Parts[0].FunctionCall.ID = "adk-confirm-106"
	confirmEv.Actions.RequestedToolConfirmations = map[string]toolconfirmation.ToolConfirmation{
		"adk-confirm-106": {Hint: "Approve Remediation Gate"},
	}
	if err := sessSvc.AppendEvent(ctx, got1.Session, confirmEv); err != nil {
		t.Fatalf("AppendEvent(#106): %v", err)
	}

	got2, err := sessSvc.Get(ctx, &session.GetRequest{
		AppName:   "sre_triage_app",
		UserID:    "sre-operator",
		SessionID: "inc-402",
	})
	if err != nil {
		t.Fatalf("second Get(inc-402): %v", err)
	}
	if got2.Session.Events().Len() != 106 {
		t.Fatalf("second Get(inc-402) events len = %d, want 106", got2.Session.Events().Len())
	}
	lastEv := got2.Session.Events().At(105)
	if _, ok := lastEv.Actions.RequestedToolConfirmations["adk-confirm-106"]; !ok {
		t.Fatalf("event #106 lost RequestedToolConfirmations: %+v", lastEv.Actions)
	}

	// 3. Direct AppendEvent against pre-existing cloud session inc-403 (without prior Get) also hydrates /api/ledger/sessions/inc-403.
	rawAppend403, _ := json.Marshal(map[string]any{
		"app_name":   "sre_triage_app",
		"user_id":    "sre-operator",
		"session_id": "inc-403",
		"event": map[string]any{
			"id":     "ev-403-1",
			"author": "sre_triage_agent",
			"content": map[string]any{
				"role":  "model",
				"parts": []map[string]any{{"text": "Recalled INC-402 pool fix"}},
			},
		},
	})
	apResp, err := ts.Client().Post(ts.URL+"/agentengine/sessions:appendEvent", "application/json", bytes.NewReader(rawAppend403))
	if err != nil {
		t.Fatalf("POST /agentengine/sessions:appendEvent(inc-403): %v", err)
	}
	_ = apResp.Body.Close()
	ledger403, err := ts.Client().Get(ts.URL + "/api/ledger/sessions/inc-403")
	if err != nil {
		t.Fatalf("GET /api/ledger/sessions/inc-403: %v", err)
	}
	_ = ledger403.Body.Close()
	if ledger403.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/ledger/sessions/inc-403 status = %d, want 200 OK after AppendEvent hydration", ledger403.StatusCode)
	}

	// 4. Cross-user AppendEvent against a pre-existing cloud session is rejected (404) without phantom hydration.
	rawWrongUser, _ := json.Marshal(map[string]any{
		"app_name":   "sre_triage_app",
		"user_id":    "intruder-user",
		"session_id": "inc-403",
		"event": map[string]any{
			"id":     "ev-intruder",
			"author": "user",
		},
	})
	badResp, err := ts.Client().Post(ts.URL+"/agentengine/sessions:appendEvent", "application/json", bytes.NewReader(rawWrongUser))
	if err != nil {
		t.Fatalf("POST /agentengine/sessions:appendEvent(wrong user): %v", err)
	}
	_ = badResp.Body.Close()
	if badResp.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong-user AppendEvent status = %d, want 404", badResp.StatusCode)
	}

	// 5. Mid-pagination upstream error on /events?pageToken=... fails instead of hydrating a truncated session.
	if _, err := sessSvc.Get(ctx, &session.GetRequest{
		AppName:   "sre_triage_app",
		UserID:    "sre-operator",
		SessionID: "inc-truncated",
	}); err == nil {
		t.Fatalf("expected Get(inc-truncated) to fail on mid-pagination 500 error")
	}
	truncLedger, err := ts.Client().Get(ts.URL + "/api/ledger/sessions/inc-truncated")
	if err != nil {
		t.Fatalf("GET /api/ledger/sessions/inc-truncated: %v", err)
	}
	_ = truncLedger.Body.Close()
	if truncLedger.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/ledger/sessions/inc-truncated status = %d, want 404 when pagination fails", truncLedger.StatusCode)
	}
}

func TestDeleteSessionPurgesUnhydratedNumericCloudSessionsByMetadataAndEvents(t *testing.T) {
	var (
		delMu      sync.Mutex
		deletedIDs = make(map[string]bool)
	)
	fakeRE := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		const prefix = "/v1beta1/projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions"
		switch {
		case r.Method == http.MethodPost && r.URL.Path == prefix:
			_, _ = w.Write([]byte(`{"done":true,"response":{"name":"projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/990033"}}`))
		case r.Method == http.MethodGet && r.URL.Path == prefix:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessions": []map[string]any{
					{
						"name":         "projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/990011",
						"userId":       "sre-operator",
						"sessionState": map[string]any{"incident_id": "INC-402"},
					},
					{
						"name":   "projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/990022",
						"userId": "sre-operator",
					},
					{
						"name":         "projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/990099",
						"userId":       "sre-operator",
						"sessionState": map[string]any{"incident_id": "INC-999"},
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == prefix+"/990022/events":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionEvents": []map[string]any{
					{
						"name":      "projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/990022/events/1",
						"author":    "user",
						"timestamp": "2026-11-15T03:00:01Z",
						"content": map[string]any{
							"role":  "user",
							"parts": []map[string]any{{"text": "Triage sibling incident INC-403 using Consolidated Memory"}},
						},
					},
					{
						"name":      "projects/demo-project/locations/us-central1/reasoningEngines/556677/sessions/990022/events/2",
						"author":    "sre_triage_agent",
						"timestamp": "2026-11-15T03:00:05Z",
						"content": map[string]any{
							"role":  "model",
							"parts": []map[string]any{{"text": "Recalled INC-402 max_open_conns=2 root cause while triaging INC-403"}},
						},
					},
				},
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, prefix+"/"):
			id := strings.TrimPrefix(r.URL.Path, prefix+"/")
			delMu.Lock()
			deletedIDs[id] = true
			delMu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakeRE.Close)

	srv, err := broker.NewServer(broker.Config{
		Namespace:                "workshop-301",
		Atespace:                 "workshop-301",
		SimNamespace:             "prod-sim",
		ProjectID:                "demo-project",
		Region:                   "us-central1",
		MockLLM:                  false,
		ReasoningEngineID:        "556677",
		AgentEngineUpstreamURL:   fakeRE.URL,
		GenAIUpstreamURL:         fakeRE.URL,
		SkillRegistryUpstreamURL: fakeRE.URL,
		AgentRegistryUpstreamURL: fakeRE.URL,
		CloudTraceUpstreamURL:    fakeRE.URL,
		StateFile:                filepath.Join(t.TempDir(), "delete-state.json"),
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	sessSvc, err := brokerclient.NewSessionService(ts.URL, ts.Client())
	if err != nil {
		t.Fatalf("NewSessionService: %v", err)
	}
	if _, err := sessSvc.Create(context.Background(), &session.CreateRequest{
		AppName:   agentruntime.AppName,
		UserID:    "sre-operator",
		SessionID: "inc-409",
	}); err != nil {
		t.Fatalf("Create(inc-409): %v", err)
	}

	deleteWorkshopSession := func(sid string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{
			"app_name":   agentruntime.AppName,
			"user_id":    "sre-operator",
			"session_id": sid,
		})
		resp, postErr := ts.Client().Post(ts.URL+"/agentengine/sessions:delete", "application/json", bytes.NewReader(body))
		if postErr != nil {
			t.Fatalf("delete(%s): %v", sid, postErr)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("delete(%s) status = %d, want 200", sid, resp.StatusCode)
		}
	}

	// Deleting inc-402 must delete 990011 (INC-402) without misclassifying sibling
	// session 990022 (INC-403) whose model reply references INC-402.
	deleteWorkshopSession("inc-402")
	delMu.Lock()
	deleted402 := deletedIDs["990011"]
	deleted403Early := deletedIDs["990022"]
	delMu.Unlock()
	if !deleted402 {
		t.Fatalf("expected 990011 (INC-402) to be deleted after delete(inc-402)")
	}
	if deleted403Early {
		t.Fatalf("sibling session 990022 (INC-403) was deleted prematurely by delete(inc-402)")
	}

	deleteWorkshopSession("inc-403")
	deleteWorkshopSession("inc-409")

	delMu.Lock()
	gotDeleted := map[string]bool{
		"990011": deletedIDs["990011"],
		"990022": deletedIDs["990022"],
		"990033": deletedIDs["990033"],
		"990099": deletedIDs["990099"],
	}
	delMu.Unlock()

	for _, wantID := range []string{"990011", "990022", "990033"} {
		if !gotDeleted[wantID] {
			t.Fatalf("expected numeric ReasoningEngine session %s to be deleted; deleted=%v", wantID, gotDeleted)
		}
	}
	if gotDeleted["990099"] {
		t.Fatalf("unrelated session 990099 (INC-999) must not be deleted")
	}

	listResp, err := ts.Client().Get(ts.URL + "/api/ledger/sessions")
	if err != nil {
		t.Fatalf("GET /api/ledger/sessions: %v", err)
	}
	var listOut struct {
		Sessions []broker.LedgerSessionOverview `json:"sessions"`
	}
	_ = json.NewDecoder(listResp.Body).Decode(&listOut)
	_ = listResp.Body.Close()
	if len(listOut.Sessions) != 0 {
		t.Fatalf("expected 0 remaining local sessions after delete, got %+v", listOut.Sessions)
	}
}
