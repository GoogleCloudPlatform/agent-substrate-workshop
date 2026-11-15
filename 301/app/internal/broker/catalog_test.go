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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/broker"
)

func TestArchitectureCatalogParityWithGlossaryAndADRs(t *testing.T) {
	cat := broker.BuildArchitectureCatalog(broker.CatalogParams{
		Namespace:       "workshop-301",
		Atespace:        "workshop-301",
		SimNamespace:    "prod-sim",
		GeminiModel:     "gemini-3.8-flash",
		DefaultTemplate: "sre-agent-v1",
		IdleAfter:       15 * time.Second,
		SPIFFEMode:      "org",
		SPIFFEDomain:    "agents.global.org-12345.system.id.goog",
	})

	if len(cat.Zones) < 3 {
		t.Fatalf("Zones count = %d, want >= 3", len(cat.Zones))
	}
	nodeIDs := map[string]bool{}
	for _, n := range cat.Nodes {
		if n.ID == "" || n.Title == "" || n.Width <= 0 || n.Height <= 0 {
			t.Errorf("invalid node entry: %+v", n)
		}
		nodeIDs[n.ID] = true
	}
	for _, reqID := range []string{
		"operator",
		"ingress-gateway",
		"sre-gateway",
		"atenet-router",
		"incident-actor",
		"gcs-snapshots",
		"agent-platform",
		"prod-sim",
	} {
		if !nodeIDs[reqID] {
			t.Errorf("ArchitectureCatalog missing required topology node %q", reqID)
		}
	}

	edgeIDs := map[string]bool{}
	for _, e := range cat.Edges {
		if !nodeIDs[e.From] || !nodeIDs[e.To] {
			t.Errorf("edge %q connects unknown endpoints %q -> %q", e.ID, e.From, e.To)
		}
		edgeIDs[e.ID] = true
	}
	if len(cat.Flows) < 3 {
		t.Errorf("Flows count = %d, want >= 3 interactive animated flows", len(cat.Flows))
	}
	for _, f := range cat.Flows {
		if len(f.Hops) == 0 {
			t.Errorf("Flow %q has zero hops", f.ID)
		}
		for _, h := range f.Hops {
			if !edgeIDs[h.EdgeID] {
				t.Errorf("Flow %q references unknown edge %q", f.ID, h.EdgeID)
			}
		}
	}
	if len(cat.Modules) != 8 {
		t.Errorf("Modules count = %d, want 8 (Module 00 through Module 07)", len(cat.Modules))
	}

	// Verify canonical domain terms from GLOSSARY.md appear in the catalog descriptions/titles.
	glossaryBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "GLOSSARY.md"))
	if err != nil {
		t.Fatalf("read GLOSSARY.md: %v", err)
	}
	glossary := string(glossaryBytes)
	blob := cat.SearchableText()
	for _, term := range []string{
		"Incident Actor",
		"Actor Boundary",
		"Governed Broker",
		"Golden Snapshot",
		"Session Ledger",
		"Consolidated Memory",
		"Procedural Skill",
		"Remediation Gate",
		"Target Service",
		"Operations Hub",
		"Incident Console",
	} {
		if !strings.Contains(glossary, term) {
			t.Fatalf("GLOSSARY.md missing expected domain term %q", term)
		}
		if !strings.Contains(strings.ToLower(blob), strings.ToLower(term)) {
			t.Errorf("ArchitectureCatalog drifted from GLOSSARY.md: missing domain term %q", term)
		}
	}
	if !strings.Contains(blob, "sre-ingress-gateway") || !strings.Contains(blob, "sre-agent-gateway") {
		t.Errorf("ArchitectureCatalog missing dual-Gateway identifiers (sre-ingress-gateway and sre-agent-gateway)")
	}
}

func TestCatalogNormalizesCloudAgentRegistryEntries(t *testing.T) {
	fakeCloudAR := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/demo-project/locations/us-central1/agents":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"agents": [
					{
						"name": "projects/demo-project/locations/us-central1/agents/sre-triage-agent",
						"displayName": "Platform SRE Triage Agent"
					},
					{
						"name": "projects/demo-project/locations/us-central1/agents/substrate-301-sre-memory",
						"displayName": "substrate-301-sre-memory"
					},
					{
						"name": "projects/demo-project/locations/us-central1/agents/8852978730",
						"displayName": ""
					},
					{
						"name": "projects/demo-project/locations/us-central1/agents/cloud-peer-agent",
						"displayName": "Cloud Peer Agent",
						"card": {
							"type": "A2A_AGENT_CARD",
							"content": {}
						}
					},
					{
						"name": "projects/demo-project/locations/us-central1/agents/",
						"displayName": "   "
					},
					{
						"name": "",
						"displayName": ""
					}
				]
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/demo-project/locations/us-central1/mcpServers":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"mcpServers":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/demo-project/locations/global/skills":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"skills":[]}`))
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
		MockLLM:                  false,
		GenAIUpstreamURL:         fakeCloudAR.URL,
		AgentEngineUpstreamURL:   fakeCloudAR.URL,
		SkillRegistryUpstreamURL: fakeCloudAR.URL,
		AgentRegistryUpstreamURL: fakeCloudAR.URL,
		CloudTraceUpstreamURL:    fakeCloudAR.URL,
	})
	if err != nil {
		t.Fatalf("broker.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/api/catalog")
	if err != nil {
		t.Fatalf("GET /api/catalog: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/catalog status = %d, want 200", resp.StatusCode)
	}

	var out struct {
		Agents []broker.RegistryAgentDTO `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode /api/catalog: %v", err)
	}

	if len(out.Agents) != 4 {
		t.Fatalf("len(agents) = %d (%+v), want 4 (self + 3 valid cloud entries, malformed empty entries skipped)", len(out.Agents), out.Agents)
	}
	for i, ag := range out.Agents {
		if strings.TrimSpace(ag.DisplayName) == "" {
			t.Fatalf("agents[%d] (%q) has blank displayName", i, ag.Name)
		}
	}
	if out.Agents[0].Source != "agent-registry-cloud+gke-auto-discovery" {
		t.Fatalf("agents[0].Source = %q, want agent-registry-cloud+gke-auto-discovery", out.Agents[0].Source)
	}
	if out.Agents[2].DisplayName != "8852978730" {
		t.Fatalf("agents[2].DisplayName = %q, want fallback agent ID 8852978730", out.Agents[2].DisplayName)
	}
	if out.Agents[2].Version != "" || len(out.Agents[2].Protocols) != 0 || out.Agents[2].Card != nil {
		t.Fatalf("expected non-A2A cloud runtime to keep empty version/protocols/card, got %+v", out.Agents[2])
	}
	if out.Agents[3].Source != "agent-registry-cloud" {
		t.Fatalf("agents[3].Source = %q, want agent-registry-cloud (distinct from GKE auto-discovery)", out.Agents[3].Source)
	}
}
