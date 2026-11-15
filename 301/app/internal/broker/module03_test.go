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
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/agentruntime"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ateclient"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ateclient/atetest"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/broker"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/brokerclient"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ledgersvc"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/opshub"
	ateapipb "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/loadmemorytool"
	"google.golang.org/adk/v2/tool/preloadmemorytool"
	"google.golang.org/adk/v2/tool/skilltoolset"
)

func buildModule03Runner(ctx context.Context, cfg agentruntime.Config, wsTools []tool.Tool) (*runner.Runner, error) {
	llm, err := brokerclient.NewModel(ctx, cfg.BrokerURL, cfg.ModelName, nil)
	if err != nil {
		return nil, err
	}
	sessionSvc, err := brokerclient.NewSessionService(cfg.BrokerURL, nil)
	if err != nil {
		return nil, err
	}
	memorySvc, err := brokerclient.NewMemoryService(cfg.BrokerURL, nil)
	if err != nil {
		return nil, err
	}
	memPlugin, err := brokerclient.NewMemoryConsolidationPlugin(sessionSvc, memorySvc)
	if err != nil {
		return nil, err
	}

	tools := append([]tool.Tool(nil), wsTools...)
	tools = append(tools, loadmemorytool.New(), preloadmemorytool.New())

	skillSource, err := brokerclient.NewSkillSource(cfg.BrokerURL, cfg.WorkspaceDir, nil)
	if err != nil {
		return nil, err
	}
	skillsTS, err := skilltoolset.New(ctx, skilltoolset.Config{
		Source: skillSource,
	})
	if err != nil {
		return nil, err
	}

	a, err := llmagent.New(llmagent.Config{
		Name:  "sre_triage_agent",
		Model: llm,
		InstructionProvider: func(agent.ReadonlyContext) (string, error) {
			return agentruntime.DefaultInstruction, nil
		},
		Tools:    tools,
		Toolsets: []tool.Toolset{skillsTS},
	})
	if err != nil {
		return nil, err
	}
	return runner.New(runner.Config{
		AppName:           agentruntime.AppName,
		Agent:             a,
		SessionService:    sessionSvc,
		MemoryService:     memorySvc,
		PluginConfig:      runner.PluginConfig{Plugins: []*plugin.Plugin{memPlugin}},
		AutoCreateSession: true,
	})
}

func makeRawZipSlipArchive(t *testing.T) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, item := range []struct {
		name string
		body string
	}{
		{name: "SKILL.md", body: "---\nname: ledger-pool-triage\ndescription: Evil skill.\n---\n# Evil\n"},
		{name: "../evil.sh", body: "#!/bin/sh\necho pwned\n"},
	} {
		w, err := zw.Create(item.name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(item.body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:])
}

func TestModule03DynamicProceduralSkillsZeroRebuildHotReload(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	v1Dir := filepath.Join(repoRoot, "modules", "03-skill-registry", "skills", "ledger-pool-triage", "v1")
	v2Dir := filepath.Join(repoRoot, "modules", "03-skill-registry", "skills", "ledger-pool-triage", "v2")

	v1Zip, v1SHA, err := broker.BuildDeterministicSkillZipFromDir(v1Dir)
	if err != nil {
		t.Fatalf("BuildDeterministicSkillZipFromDir(v1): %v", err)
	}
	v2Zip, v2SHA, err := broker.BuildDeterministicSkillZipFromDir(v2Dir)
	if err != nil {
		t.Fatalf("BuildDeterministicSkillZipFromDir(v2): %v", err)
	}
	if v1SHA == v2SHA || len(v1Zip) == 0 || len(v2Zip) == 0 {
		t.Fatalf("expected distinct non-empty archives for v1 (%s) and v2 (%s)", v1SHA, v2SHA)
	}

	// Verify byte-for-byte parity between Go BuildDeterministicSkillZipFromDir and scripts/publish-skill.sh --build-only.
	cliZipPath := filepath.Join(t.TempDir(), "cli-v1.zip")
	cmdOut, err := exec.Command("bash", filepath.Join(repoRoot, "scripts", "publish-skill.sh"), "--build-only", v1Dir, cliZipPath).CombinedOutput()
	if err != nil {
		t.Fatalf("publish-skill.sh --build-only failed: %v (%s)", err, string(cmdOut))
	}
	if gotCLISHA := strings.TrimSpace(string(cmdOut)); gotCLISHA != v1SHA {
		t.Fatalf("CLI zip SHA-256 = %q, want Go parity SHA-256 %q", gotCLISHA, v1SHA)
	}

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

	// Create ONE persistent Incident Actor (sre-agent-v3) whose /workspace survives across
	// suspend/resume turns without ever rebuilding or restarting the Actor!
	actorWorkspaceDir := t.TempDir()
	var actorInstancesCreated atomic.Int64
	var singleActorTS *httptest.Server
	getSingleActor := func() *httptest.Server {
		if singleActorTS != nil {
			return singleActorTS
		}
		actorInstancesCreated.Add(1)
		srv, err := agentruntime.NewServer(agentruntime.Config{
			WorkspaceDir: actorWorkspaceDir,
			BrokerURL:    brokerURL,
			ModelName:    "gemini-3.8-flash",
			TemplateName: "sre-agent-v3",
			BuildRunner:  buildModule03Runner,
		})
		if err != nil {
			t.Fatalf("agentruntime.NewServer(sre-agent-v3): %v", err)
		}
		singleActorTS = httptest.NewServer(srv.Handler())
		t.Cleanup(singleActorTS.Close)
		return singleActorTS
	}

	routerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fakeATE.MarkRunning("workshop-301", "inc-402", "sre-workers-0")
		targetTS := getSingleActor()
		fwdReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetTS.URL+r.URL.RequestURI(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fwdReq.Header = r.Header.Clone()
		resp, err := targetTS.Client().Do(fwdReq)
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
		Namespace:       "workshop-301",
		Atespace:        "workshop-301",
		SimNamespace:    "prod-sim",
		ProjectID:       "demo-project",
		Region:          "us-central1",
		ModelLocation:   "global",
		GeminiModel:     "gemini-3.8-flash",
		MockLLM:         true,
		DefaultTemplate: "sre-agent-v3",
		SelfURL:         brokerURL,
		RouterURL:       routerTS.URL,
		OpsHubURL:       opsTS.URL,
		LedgerAPIURL:    ledgerTS.URL,
		IdleAfter:       35 * time.Millisecond,
		SPIFFEMode:      "wif",
		SPIFFEDomain:    "demo-project.svc.id.goog",
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

	// 1. Verify pre-seeded v1 in /api/catalog and parity with v1SHA.
	catResp, err := http.Get(brokerURL + "/api/catalog")
	if err != nil {
		t.Fatalf("GET /api/catalog: %v", err)
	}
	var catDoc struct {
		Skills []broker.CatalogSkillSummary `json:"skills"`
	}
	if err := json.NewDecoder(catResp.Body).Decode(&catDoc); err != nil {
		catResp.Body.Close()
		t.Fatalf("decode /api/catalog: %v", err)
	}
	catResp.Body.Close()
	if len(catDoc.Skills) != 1 || catDoc.Skills[0].Name != "ledger-pool-triage" || catDoc.Skills[0].DefaultRevision != "v1" || catDoc.Skills[0].ActiveSHA256 != v1SHA {
		t.Fatalf("initial /api/catalog skills = %+v, want ledger-pool-triage@v1 (%s)", catDoc.Skills, v1SHA)
	}

	// 2. Verify fail-closed upload checks (SHA-256 mismatch, zip-slip, immutable overwrite, CSRF).
	badHashBody, _ := json.Marshal(map[string]any{
		"skill":          "ledger-pool-triage",
		"revision":       "v99",
		"sha256":         strings.Repeat("f", 64),
		"archive_base64": base64.StdEncoding.EncodeToString(v2Zip),
		"set_default":    true,
	})
	badHashResp, err := http.Post(brokerURL+"/api/catalog/skills", "application/json", bytes.NewReader(badHashBody))
	if err != nil {
		t.Fatal(err)
	}
	badHashResp.Body.Close()
	if badHashResp.StatusCode != http.StatusBadRequest {
		t.Errorf("mismatched SHA-256 upload status = %d, want 400", badHashResp.StatusCode)
	}

	evilZip, evilSHA := makeRawZipSlipArchive(t)
	zipSlipBody, _ := json.Marshal(map[string]any{
		"skill":          "ledger-pool-triage",
		"revision":       "v98",
		"sha256":         evilSHA,
		"archive_base64": base64.StdEncoding.EncodeToString(evilZip),
		"set_default":    true,
	})
	zipSlipResp, err := http.Post(brokerURL+"/api/catalog/skills", "application/json", bytes.NewReader(zipSlipBody))
	if err != nil {
		t.Fatal(err)
	}
	zipSlipResp.Body.Close()
	if zipSlipResp.StatusCode != http.StatusBadRequest {
		t.Errorf("zip-slip upload status = %d, want 400", zipSlipResp.StatusCode)
	}

	overwriteV1Body, _ := json.Marshal(map[string]any{
		"skill":          "ledger-pool-triage",
		"revision":       "v1",
		"sha256":         v2SHA,
		"archive_base64": base64.StdEncoding.EncodeToString(v2Zip),
		"set_default":    true,
	})
	overwriteResp, err := http.Post(brokerURL+"/api/catalog/skills", "application/json", bytes.NewReader(overwriteV1Body))
	if err != nil {
		t.Fatal(err)
	}
	overwriteResp.Body.Close()
	if overwriteResp.StatusCode != http.StatusConflict {
		t.Errorf("immutable revision overwrite status = %d, want 409", overwriteResp.StatusCode)
	}

	postChat := func(prompt string) broker.ChatResponse {
		t.Helper()
		b, _ := json.Marshal(broker.ChatRequest{
			IncidentID: "INC-402",
			Template:   "sre-agent-v3",
			Prompt:     prompt,
		})
		resp, err := http.Post(brokerURL+"/api/chat", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("POST /api/chat: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("POST /api/chat status = %d: %s", resp.StatusCode, string(raw))
		}
		var out broker.ChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode ChatResponse: %v", err)
		}
		return out
	}

	// 3. Turn 1: Incident Actor inc-402 (sre-agent-v3) discovers, verifies, unpacks, and runs v1!
	turn1 := postChat("Run the ledger-pool-triage procedural skill against INC-402 and report the SKILL_REVISION findings.")
	var sawLoadSkill1, sawSh1 bool
	for _, st := range turn1.ToolSteps {
		if st.Name == "load_skill" {
			sawLoadSkill1 = true
		}
		if st.Name == "run_workspace_command" && strings.Contains(st.Args, "diagnose_pool.sh") {
			sawSh1 = true
		}
	}
	if !sawLoadSkill1 || !sawSh1 {
		t.Fatalf("Turn 1 ToolSteps = %+v; want both load_skill and run_workspace_command(diagnose_pool.sh)", turn1.ToolSteps)
	}
	if !strings.Contains(turn1.Reply, "SKILL_REVISION=v1") || !strings.Contains(turn1.Reply, "MAX_OPEN_CONNS=2") {
		t.Fatalf("Turn 1 Reply = %q; want SKILL_REVISION=v1 and MAX_OPEN_CONNS=2", turn1.Reply)
	}
	if strings.Contains(turn1.Reply, "SKILL_REVISION=v2") {
		t.Fatalf("Turn 1 Reply unexpectedly reported SKILL_REVISION=v2 before v2 was published")
	}

	// Suspend inc-402 to 0 vCPU between turns (normal actor lifecycle, zero rebuild!).
	time.Sleep(50 * time.Millisecond)
	if err := gwSrv.RunIdleTick(context.Background()); err != nil {
		t.Fatalf("RunIdleTick: %v", err)
	}
	if a := fakeATE.Actor("workshop-301", "inc-402"); a == nil || a.GetStatus().GetState() != ateapipb.ActorState_ACTOR_STATE_SUSPENDED {
		t.Fatalf("inc-402 state = %v, want ACTOR_STATE_SUSPENDED", a)
	}

	// 4. Publish v2 and flip defaultRevision to v2 WITHOUT rebuilding or restarting inc-402!
	pubV2Body, _ := json.Marshal(map[string]any{
		"skill":          "ledger-pool-triage",
		"revision":       "v2",
		"sha256":         v2SHA,
		"archive_base64": base64.StdEncoding.EncodeToString(v2Zip),
		"set_default":    true,
	})
	pubResp, err := http.Post(brokerURL+"/api/catalog/skills", "application/json", bytes.NewReader(pubV2Body))
	if err != nil {
		t.Fatalf("POST /api/catalog/skills (v2): %v", err)
	}
	var pubOut broker.CatalogSkillSummary
	if err := json.NewDecoder(pubResp.Body).Decode(&pubOut); err != nil {
		pubResp.Body.Close()
		t.Fatal(err)
	}
	pubResp.Body.Close()
	if pubResp.StatusCode != http.StatusOK || pubOut.DefaultRevision != "v2" || pubOut.ActiveSHA256 != v2SHA || len(pubOut.Revisions) != 2 {
		t.Fatalf("POST /api/catalog/skills (v2) status=%d out=%+v; want default_revision=v2 with 2 revisions", pubResp.StatusCode, pubOut)
	}

	// 5. Turn 2: Resume the EXACT SAME sre-agent-v3 Actor and verify v2 executes dynamically!
	turn2 := postChat("Re-run the ledger-pool-triage procedural skill on INC-402 and report the updated SKILL_REVISION findings.")
	if gotInstances := actorInstancesCreated.Load(); gotInstances != 1 {
		t.Fatalf("actorInstancesCreated = %d, want 1 (zero Actor recreation or Golden Snapshot churn)", gotInstances)
	}
	var sawLoadSkill2, sawSh2 bool
	for _, st := range turn2.ToolSteps {
		if st.Name == "load_skill" {
			sawLoadSkill2 = true
		}
		if st.Name == "run_workspace_command" && strings.Contains(st.Args, "diagnose_pool.sh") {
			sawSh2 = true
		}
	}
	if !sawLoadSkill2 || !sawSh2 {
		t.Fatalf("Turn 2 ToolSteps = %+v; want both load_skill and run_workspace_command(diagnose_pool.sh)", turn2.ToolSteps)
	}
	for _, want := range []string{"SKILL_REVISION=v2", "CURSOR_LEAK_DETECTED=true", "ReconcileTransfer"} {
		if !strings.Contains(turn2.Reply, want) {
			t.Errorf("Turn 2 Reply missing %q:\n%s", want, turn2.Reply)
		}
	}

	// Verify both versioned directories and active pointer inside the Actor's /workspace/.skills/!
	for _, p := range []string{
		filepath.Join(actorWorkspaceDir, ".skills", "ledger-pool-triage@v1", "scripts", "diagnose_pool.sh"),
		filepath.Join(actorWorkspaceDir, ".skills", "ledger-pool-triage@v2", "scripts", "diagnose_pool.sh"),
		filepath.Join(actorWorkspaceDir, ".skills", "ledger-pool-triage", "scripts", "diagnose_pool.sh"),
	} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("missing unpacked skill script %s: %v", p, err)
		}
		if st.Mode()&0o111 == 0 {
			t.Errorf("skill script %s mode = %v, want executable (0755)", p, st.Mode())
		}
	}

	// 6. Verify CSRF-protected defaultRevision switch via /api/session + POST /api/catalog/skills,
	// and verify /catalog HTML serves the Skill Registry table and revision drawer.
	sessResp, err := http.Get(brokerURL + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	var sessDoc struct {
		CSRFToken string `json:"csrf_token"`
	}
	_ = json.NewDecoder(sessResp.Body).Decode(&sessDoc)
	cookies := sessResp.Cookies()
	sessResp.Body.Close()

	noCSRFReq, _ := http.NewRequest(http.MethodPost, brokerURL+"/api/catalog/skills", strings.NewReader(`{"skill":"ledger-pool-triage","default_revision":"v1"}`))
	noCSRFReq.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		noCSRFReq.AddCookie(c)
	}
	noCSRFResp, err := http.DefaultClient.Do(noCSRFReq)
	if err != nil {
		t.Fatal(err)
	}
	noCSRFResp.Body.Close()
	if noCSRFResp.StatusCode != http.StatusForbidden {
		t.Errorf("cookie POST /api/catalog/skills without CSRF status = %d, want 403", noCSRFResp.StatusCode)
	}

	csrfReq, _ := http.NewRequest(http.MethodPost, brokerURL+"/api/catalog/skills", strings.NewReader(`{"skill":"ledger-pool-triage","default_revision":"v1"}`))
	csrfReq.Header.Set("Content-Type", "application/json")
	csrfReq.Header.Set("X-CSRF-Token", sessDoc.CSRFToken)
	for _, c := range cookies {
		csrfReq.AddCookie(c)
	}
	csrfResp, err := http.DefaultClient.Do(csrfReq)
	if err != nil {
		t.Fatal(err)
	}
	csrfResp.Body.Close()
	if csrfResp.StatusCode != http.StatusOK {
		t.Errorf("CSRF-authenticated POST /api/catalog/skills status = %d, want 200", csrfResp.StatusCode)
	}

	catalogHTMLResp, err := http.Get(brokerURL + "/catalog")
	if err != nil {
		t.Fatal(err)
	}
	catalogHTMLBytes, _ := io.ReadAll(catalogHTMLResp.Body)
	catalogHTMLResp.Body.Close()
	catalogHTML := string(catalogHTMLBytes)
	for _, marker := range []string{"Skill &amp; agent catalog", "Procedural skills", "skills-registry-body", "skill-revision-drawer"} {
		if !strings.Contains(catalogHTML, marker) {
			t.Errorf("/catalog HTML missing %q", marker)
		}
	}
}

func TestSkillRegistryLiveCloudUpstreamDelegation(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	v2Dir := filepath.Join(repoRoot, "modules", "03-skill-registry", "skills", "ledger-pool-triage", "v2")
	v2Zip, v2SHA, err := broker.BuildDeterministicSkillZipFromDir(v2Dir)
	if err != nil {
		t.Fatal(err)
	}

	var cloudHits atomic.Int64
	cloudRegistryTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudHits.Add(1)
		const base = "/v1/projects/demo-project/locations/global/skills"
		switch {
		case r.URL.Path == base:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"skills": []map[string]any{
					{
						"name":            "projects/demo-project/locations/global/skills/ledger-pool-triage",
						"displayName":     "ledger-pool-triage",
						"description":     "Cloud Skill Registry v2",
						"defaultRevision": "projects/demo-project/locations/global/skills/ledger-pool-triage/revisions/v2",
						"state":           "STATE_ACTIVE",
						"frontmatter": map[string]any{
							"name":        "ledger-pool-triage",
							"description": "Cloud Skill Registry v2",
						},
					},
				},
			})
		case r.URL.Path == base+"/ledger-pool-triage":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":            "projects/demo-project/locations/global/skills/ledger-pool-triage",
				"defaultRevision": "projects/demo-project/locations/global/skills/ledger-pool-triage/revisions/v2",
			})
		case r.URL.Path == base+"/ledger-pool-triage/revisions/v2":
			if r.URL.Query().Get("alt") == "media" {
				w.Header().Set("Content-Type", "application/zip")
				_, _ = w.Write(v2Zip)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":       "projects/demo-project/locations/global/skills/ledger-pool-triage/revisions/v2",
				"sha256Hash": v2SHA,
				"sizeBytes":  len(v2Zip),
				"state":      "ACTIVE",
			})
		case r.URL.Path == "/download"+base+"/ledger-pool-triage/revisions/v2":
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(v2Zip)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cloudRegistryTS.Close)

	genaiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(genaiStub.Close)

	gwSrv, err := broker.NewServer(broker.Config{
		Namespace:                "workshop-301",
		Atespace:                 "workshop-301",
		SimNamespace:             "prod-sim",
		ProjectID:                "demo-project",
		Region:                   "us-central1",
		MockLLM:                  false,
		GenAIUpstreamURL:         genaiStub.URL,
		SkillRegistryUpstreamURL: cloudRegistryTS.URL,
		CloudTraceUpstreamURL:    genaiStub.URL,
	})
	if err != nil {
		t.Fatalf("broker.NewServer(cloud upstream): %v", err)
	}
	t.Cleanup(func() { _ = gwSrv.Close() })

	gwTS := httptest.NewServer(gwSrv.Handler())
	t.Cleanup(gwTS.Close)

	src, err := brokerclient.NewSkillSource(gwTS.URL, t.TempDir(), gwTS.Client())
	if err != nil {
		t.Fatal(err)
	}
	inst, err := src.LoadInstructions(context.Background(), "ledger-pool-triage")
	if err != nil {
		t.Fatalf("LoadInstructions via Cloud upstream delegation: %v", err)
	}
	if !strings.Contains(inst, "Revision v2") {
		t.Errorf("LoadInstructions = %q, want Revision v2 from Cloud upstream", inst)
	}
	if cloudHits.Load() == 0 {
		t.Errorf("expected cloudHits > 0 against SkillRegistryUpstreamURL")
	}
}
