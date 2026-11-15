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
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gke-demos/substrate-agent-platform/app/internal/agentruntime"
	"github.com/gke-demos/substrate-agent-platform/app/internal/ateclient"
	"github.com/gke-demos/substrate-agent-platform/app/internal/ateclient/atetest"
	"github.com/gke-demos/substrate-agent-platform/app/internal/broker"
	"github.com/gke-demos/substrate-agent-platform/app/internal/ledgersvc"
	"github.com/gke-demos/substrate-agent-platform/app/internal/opshub"
)

func TestModule06W3CTraceparentWaterfallScrubbingCloudTraceAndMetrics(t *testing.T) {
	var (
		cloudTraceMu      sync.Mutex
		cloudTraceBatches []string
		otlpPostCount     atomic.Int64
		otlpProtoCount    atomic.Int64
	)
	cloudTraceTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/projects/demo-project/traces:batchWrite" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		cloudTraceMu.Lock()
		cloudTraceBatches = append(cloudTraceBatches, string(raw))
		cloudTraceMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(cloudTraceTS.Close)

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

	var (
		routerTraceparentsMu sync.Mutex
		routerTraceparents   []string
		actorTraceparentsMu  sync.Mutex
		actorTraceparents    []string
	)
	actorWorkspaceDir := t.TempDir()
	var actorTS *httptest.Server
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
			if r.URL.Path == "/turn" {
				actorTraceparentsMu.Lock()
				actorTraceparents = append(actorTraceparents, r.Header.Get("traceparent"))
				actorTraceparentsMu.Unlock()
			}
			srv.Handler().ServeHTTP(w, r)
		}))
		t.Cleanup(actorTS.Close)
		return actorTS
	}

	routerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routerTraceparentsMu.Lock()
		routerTraceparents = append(routerTraceparents, r.Header.Get("traceparent"))
		routerTraceparentsMu.Unlock()
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
		CloudTraceUpstreamURL: cloudTraceTS.URL,
		IdleAfter:             10 * time.Minute,
		AgentIdentityCertFile: svidFile,
		GovernanceEnforced:    true,
		Require3LO:            true,
		OAuthClientID:         "sre-gateway-3lo",
		OAuthClientSecret:     "super-secret-oauth-client-secret-9911",
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

	baseHandler := gwSrv.Handler()
	httpSrv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/otlp/v1/traces" && r.Method == http.MethodPost {
				otlpPostCount.Add(1)
				if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/x-protobuf") {
					otlpProtoCount.Add(1)
				}
			}
			baseHandler.ServeHTTP(w, r)
		}),
	}
	go func() { _ = httpSrv.Serve(gwLis) }()
	t.Cleanup(func() { _ = httpSrv.Close() })

	// 1. Dispatch gated INC-402 turn with an explicit W3C traceparent header.
	const wantTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	const incomingTP = "00-" + wantTraceID + "-00f067aa0ba902b7-01"

	stagePayload, _ := json.Marshal(broker.ChatRequest{
		IncidentID: "INC-402",
		Template:   "sre-agent-v4",
		Prompt:     "Stage and remediate INC-402 reported by alice.secops@corp.example.com: query MCP logs and health, patch config/pool.yaml and pool/store.go on branch fix/inc-402-pool-leak, verify with ledger-verify, push the branch, and apply_remediation_pr.",
	})
	chatHTTPReq, _ := http.NewRequest(http.MethodPost, brokerURL+"/api/chat", bytes.NewReader(stagePayload))
	chatHTTPReq.Header.Set("Content-Type", "application/json")
	chatHTTPReq.Header.Set("traceparent", incomingTP)
	stageResp, err := http.DefaultClient.Do(chatHTTPReq)
	if err != nil {
		t.Fatalf("POST /api/chat: %v", err)
	}
	stageRaw, _ := io.ReadAll(stageResp.Body)
	stageResp.Body.Close()
	if stageResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/chat status = %d: %s", stageResp.StatusCode, string(stageRaw))
	}

	// Verify router and actor both received W3C traceparent with wantTraceID.
	routerTraceparentsMu.Lock()
	gotRouterTPs := append([]string(nil), routerTraceparents...)
	routerTraceparentsMu.Unlock()
	if len(gotRouterTPs) == 0 || !strings.HasPrefix(gotRouterTPs[0], "00-"+wantTraceID+"-") {
		t.Fatalf("router traceparents = %v, want prefix 00-%s-", gotRouterTPs, wantTraceID)
	}
	actorTraceparentsMu.Lock()
	gotActorTPs := append([]string(nil), actorTraceparents...)
	actorTraceparentsMu.Unlock()
	if len(gotActorTPs) == 0 || !strings.HasPrefix(gotActorTPs[0], "00-"+wantTraceID+"-") {
		t.Fatalf("actor traceparents = %v, want prefix 00-%s-", gotActorTPs, wantTraceID)
	}
	if otlpPostCount.Load() == 0 || otlpProtoCount.Load() == 0 {
		t.Fatalf("expected agentruntime to POST keyless OTLP protobuf spans via otlptracehttp to /otlp/v1/traces, got total=%d proto=%d", otlpPostCount.Load(), otlpProtoCount.Load())
	}

	// 2. Authorize 3LO and approve the Remediation Gate under a second traceparent so auth_manager.inject_3lo is recorded.
	startResp, err := http.Post(brokerURL+"/api/oauth/start", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /api/oauth/start: %v", err)
	}
	var startDoc struct {
		AuthURL string `json:"auth_url"`
	}
	_ = json.NewDecoder(startResp.Body).Decode(&startDoc)
	startResp.Body.Close()
	completeBrowserPKCEFlow(t, startDoc.AuthURL)

	const approveTraceID = "7b2299887766554433221100aabbccdd"
	appReq, _ := http.NewRequest(http.MethodPost, brokerURL+"/api/incidents/INC-402/remediation/approve", strings.NewReader(`{}`))
	appReq.Header.Set("Content-Type", "application/json")
	appReq.Header.Set("traceparent", "00-"+approveTraceID+"-1122334455667788-01")
	appResp, err := http.DefaultClient.Do(appReq)
	if err != nil {
		t.Fatalf("POST /remediation/approve: %v", err)
	}
	appRaw, _ := io.ReadAll(appResp.Body)
	appResp.Body.Close()
	if appResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /remediation/approve status = %d: %s", appResp.StatusCode, string(appRaw))
	}

	// 3. Also trigger INC-409 Model Armor block and post a synthetic dirty OTLP payload containing raw credentials.
	const blockedTraceID = "99887766554433221100ffeeddccbbaa"
	inc409Payload, _ := json.Marshal(broker.ChatRequest{
		IncidentID: "INC-409",
		Template:   "sre-agent-v4",
		Prompt:     "Investigate INC-409.",
	})
	req409, _ := http.NewRequest(http.MethodPost, brokerURL+"/api/chat", bytes.NewReader(inc409Payload))
	req409.Header.Set("Content-Type", "application/json")
	req409.Header.Set("traceparent", "00-"+blockedTraceID+"-aabbccddeeff0011-01")
	resp409, err := http.DefaultClient.Do(req409)
	if err != nil {
		t.Fatalf("POST /api/chat INC-409: %v", err)
	}
	resp409.Body.Close()
	if resp409.StatusCode != http.StatusForbidden {
		t.Fatalf("INC-409 status = %d, want 403", resp409.StatusCode)
	}

	fakeGCPTok1 := "ya29" + ".RawGoogleAccessTokenLeak12345"
	fakeGCPTok2 := "ya29" + ".RawToolResponseSecret"
	fakeGHOTok := "gho" + "_SecretGitHubTokenValue9999"
	fakeOpsTok1 := "opshub_at" + "_Secret3LOToken8888"
	fakeOpsTok2 := "opshub_at" + "_RawToolArgSecret"
	dirtyOTLP := map[string]any{
		"resourceSpans": []map[string]any{
			{
				"scopeSpans": []map[string]any{
					{
						"spans": []map[string]any{
							{
								"traceId":           wantTraceID,
								"spanId":            "deadbeefcafebabe",
								"parentSpanId":      "00f067aa0ba902b7",
								"name":              "execute_tool dirty_probe",
								"startTimeUnixNano": "1763119200000000000",
								"endTimeUnixNano":   "1763119200050000000",
								"attributes": []map[string]any{
									{"key": "http.request.header.authorization", "value": map[string]any{"stringValue": "Bearer " + fakeGCPTok1}},
									{"key": "http.request.header.cookie", "value": map[string]any{"stringValue": "sre_csrf=secretcookievalue"}},
									{"key": "github.token_note", "value": map[string]any{"stringValue": "token " + fakeGHOTok + " and " + fakeOpsTok1 + " and super-secret-oauth-client-secret-9911"}},
									{"key": "gcp.vertex.agent.tool_call_args", "value": map[string]any{"stringValue": `{"secret":"` + fakeOpsTok2 + `"}`}},
									{"key": "gcp.vertex.agent.tool_response", "value": map[string]any{"stringValue": `{"raw_blob":"` + fakeGCPTok2 + `"}`}},
									{"key": "gen_ai.tool.name", "value": map[string]any{"stringValue": "dirty_probe"}},
								},
							},
						},
					},
				},
			},
		},
	}
	dirtyBytes, _ := json.Marshal(dirtyOTLP)
	otlpResp, err := http.Post(brokerURL+"/otlp/v1/traces", "application/json", bytes.NewReader(dirtyBytes))
	if err != nil {
		t.Fatalf("POST /otlp/v1/traces: %v", err)
	}
	otlpResp.Body.Close()
	if otlpResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /otlp/v1/traces status = %d, want 200", otlpResp.StatusCode)
	}

	// 4. Inspect GET /api/traces/{trace_id} for both the gated turn and the 3LO approval turn.
	tr1Resp, err := http.Get(brokerURL + "/api/traces/" + wantTraceID)
	if err != nil {
		t.Fatalf("GET /api/traces/%s: %v", wantTraceID, err)
	}
	tr1Raw, _ := io.ReadAll(tr1Resp.Body)
	tr1Resp.Body.Close()
	if tr1Resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/traces/%s status = %d: %s", wantTraceID, tr1Resp.StatusCode, string(tr1Raw))
	}

	tr2Resp, err := http.Get(brokerURL + "/api/traces/" + approveTraceID)
	if err != nil {
		t.Fatalf("GET /api/traces/%s: %v", approveTraceID, err)
	}
	tr2Raw, _ := io.ReadAll(tr2Resp.Body)
	tr2Resp.Body.Close()
	if tr2Resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/traces/%s status = %d: %s", approveTraceID, tr2Resp.StatusCode, string(tr2Raw))
	}

	type spanDTO struct {
		TraceID      string         `json:"trace_id"`
		SpanID       string         `json:"span_id"`
		ParentSpanID string         `json:"parent_span_id"`
		Name         string         `json:"name"`
		Boundary     string         `json:"boundary"`
		WidthPct     float64        `json:"width_pct"`
		Attributes   map[string]any `json:"attributes"`
	}
	type traceDTO struct {
		TraceID      string    `json:"trace_id"`
		IncidentID   string    `json:"incident_id"`
		SessionID    string    `json:"session_id"`
		AgentURN     string    `json:"agent_urn"`
		InputTokens  int64     `json:"input_tokens"`
		OutputTokens int64     `json:"output_tokens"`
		Spans        []spanDTO `json:"spans"`
	}
	var trace1, trace2 traceDTO
	if err := json.Unmarshal(tr1Raw, &trace1); err != nil {
		t.Fatalf("decode trace1: %v", err)
	}
	if err := json.Unmarshal(tr2Raw, &trace2); err != nil {
		t.Fatalf("decode trace2: %v", err)
	}

	seenNames := map[string]bool{}
	var rootAttrs, genaiAttrs map[string]any
	for _, sp := range append(append([]spanDTO(nil), trace1.Spans...), trace2.Spans...) {
		seenNames[sp.Name] = true
		if sp.Name == "invoke_gateway_turn" && rootAttrs == nil {
			rootAttrs = sp.Attributes
		}
		if sp.Name == "genai.generate_content" && genaiAttrs == nil {
			genaiAttrs = sp.Attributes
		}
		if sp.WidthPct <= 0 {
			t.Errorf("span %q has non-positive WidthPct=%v", sp.Name, sp.WidthPct)
		}
	}

	for _, wantSpan := range []string{
		"invoke_gateway_turn",
		"model_armor.screen",
		"atenet.wake_and_route",
		"invoke_agent",
		"execute_tool",
		"genai.generate_content",
		"mcp.call_tool",
		"auth_manager.inject_3lo",
		"substrate.suspend_actor",
	} {
		if !seenNames[wantSpan] {
			t.Errorf("missing required waterfall span %q (saw %v)", wantSpan, seenNames)
		}
	}

	wantURN := "projects/demo-project/locations/us-central1/agents/platform-sre-triage-agent"
	if got := rootAttrs["gen_ai.system"]; got != "gemini_enterprise_agent_platform" {
		t.Errorf("gen_ai.system = %v, want gemini_enterprise_agent_platform", got)
	}
	if got := rootAttrs["gen_ai.agent.id"]; got != wantURN {
		t.Errorf("gen_ai.agent.id = %v, want %s", got, wantURN)
	}
	if got := rootAttrs["gen_ai.agent.name"]; got != "platform-sre-triage-agent" {
		t.Errorf("gen_ai.agent.name = %v, want platform-sre-triage-agent", got)
	}
	if got := rootAttrs["gen_ai.conversation.id"]; got != "inc-402" {
		t.Errorf("gen_ai.conversation.id = %v, want inc-402", got)
	}
	if got := rootAttrs["ate.actor.id"]; got != "inc-402" {
		t.Errorf("ate.actor.id = %v, want inc-402", got)
	}
	if got := rootAttrs["ate.atespace"]; got != "workshop-301" {
		t.Errorf("ate.atespace = %v, want workshop-301", got)
	}
	if got := genaiAttrs["gen_ai.request.model"]; got != "gemini-3.8-flash" {
		t.Errorf("gen_ai.request.model = %v, want gemini-3.8-flash", got)
	}
	if trace1.InputTokens <= 0 || trace1.OutputTokens <= 0 {
		t.Errorf("trace1 token totals = (%d, %d), want > 0", trace1.InputTokens, trace1.OutputTokens)
	}

	// 5. Verify blocked INC-409 trace was also captured with status=ERROR.
	tr3Resp, err := http.Get(brokerURL + "/api/traces/" + blockedTraceID)
	if err != nil {
		t.Fatalf("GET /api/traces/%s: %v", blockedTraceID, err)
	}
	tr3Raw, _ := io.ReadAll(tr3Resp.Body)
	tr3Resp.Body.Close()
	if tr3Resp.StatusCode != http.StatusOK || !strings.Contains(string(tr3Raw), "model_armor.screen") {
		t.Errorf("blocked trace %s = %d (%s), want 200 containing model_armor.screen", blockedTraceID, tr3Resp.StatusCode, string(tr3Raw))
	}

	// 6. Verify Cloud Trace batchWrite export and 100% secret/payload scrubbing across ALL outputs.
	allListResp, err := http.Get(brokerURL + "/api/traces")
	if err != nil {
		t.Fatalf("GET /api/traces: %v", err)
	}
	allListRaw, _ := io.ReadAll(allListResp.Body)
	allListResp.Body.Close()

	cloudTraceMu.Lock()
	exportedBlob := strings.Join(cloudTraceBatches, "\n")
	batchCount := len(cloudTraceBatches)
	cloudTraceMu.Unlock()
	if batchCount == 0 {
		t.Fatalf("expected Cloud Trace v2 traces:batchWrite batches, got 0")
	}
	if !strings.Contains(exportedBlob, "projects/demo-project/traces/"+wantTraceID+"/spans/") {
		t.Errorf("Cloud Trace export missing expected span resource name for trace %s", wantTraceID)
	}

	combinedInspect := string(tr1Raw) + "\n" + string(tr2Raw) + "\n" + string(allListRaw) + "\n" + exportedBlob
	for _, forbidden := range []string{
		"ya29.",
		"gho_",
		"opshub_at_",
		"sre_csrf=",
		"super-secret-oauth-client-secret-9911",
		"alice.secops@corp.example.com",
		"gcp.vertex.agent.tool_call_args",
		"gcp.vertex.agent.tool_response",
		"RawToolArgSecret",
		"RawToolResponseSecret",
	} {
		if strings.Contains(combinedInspect, forbidden) {
			t.Errorf("sensitive string %q leaked into stored or exported trace spans!", forbidden)
		}
	}

	// 7. Verify GET /metrics exposes low-cardinality ActorTemplate Prometheus metrics without actor/session IDs.
	metricsResp, err := http.Get(brokerURL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	metricsRaw, _ := io.ReadAll(metricsResp.Body)
	metricsResp.Body.Close()
	if metricsResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", metricsResp.StatusCode)
	}
	metricsText := string(metricsRaw)
	for _, wantMetric := range []string{
		`sre_gateway_turns_total{`,
		`template="sre-agent-v4"`,
		`sre_gateway_genai_tokens_total{`,
		`sre_gateway_model_armor_verdicts_total{`,
		`sre_gateway_otel_spans_exported_total{`,
	} {
		if !strings.Contains(metricsText, wantMetric) {
			t.Errorf("GET /metrics missing %q:\n%s", wantMetric, metricsText)
		}
	}
	for _, highCard := range []string{"inc-402", "inc-409", wantTraceID, "session_id=", "actor_id="} {
		if strings.Contains(metricsText, highCard) {
			t.Errorf("GET /metrics leaked high-cardinality identifier %q:\n%s", highCard, metricsText)
		}
	}
}
