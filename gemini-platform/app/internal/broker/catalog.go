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
	"strings"
	"time"
)

// CatalogParams supplies live runtime coordinates for BuildArchitectureCatalog.
type CatalogParams struct {
	Namespace            string
	Atespace             string
	SimNamespace         string
	GeminiModel          string
	DefaultTemplate      string
	IdleAfter            time.Duration
	SPIFFEMode           string
	SPIFFEDomain         string
	SPIFFEPrincipal      string
	SVIDMounted          bool
	GovernanceEnforced   bool
	ModelArmorTemplateID string
	Require3LO           bool
	GCPAuthProviderID    string
}

// ArchitectureZone describes a visual trust boundary swimlane on /about.
type ArchitectureZone struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// ArchitectureNode describes one component on the /about SVG topology canvas.
type ArchitectureNode struct {
	ID         string   `json:"id"`
	ZoneID     string   `json:"zone_id"`
	Title      string   `json:"title"`
	Subtitle   string   `json:"subtitle"`
	Badge      string   `json:"badge"`
	Summary    string   `json:"summary"`
	Invariants []string `json:"invariants"`
	X          int      `json:"x"`
	Y          int      `json:"y"`
	Width      int      `json:"width"`
	Height     int      `json:"height"`
}

// ArchitectureEdge describes a directed wire hop across or inside the Actor Boundary.
type ArchitectureEdge struct {
	ID          string `json:"id"`
	From        string `json:"from"`
	To          string `json:"to"`
	Label       string `json:"label"`
	Protocol    string `json:"protocol"`
	CrossesGate bool   `json:"crosses_actor_boundary"`
}

// ArchitectureHop describes one animated pulse step in an interactive topology flow.
type ArchitectureHop struct {
	Step    int    `json:"step"`
	EdgeID  string `json:"edge_id"`
	Caption string `json:"caption"`
	Tone    string `json:"tone"`
}

// ArchitectureFlow describes an interactive animated scenario on /about.
type ArchitectureFlow struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Hops        []ArchitectureHop `json:"hops"`
}

// ModuleStage summarizes one of the eight workshop modules (00 through 07).
type ModuleStage struct {
	Number  string `json:"number"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// ArchitectureCatalog is the authoritative topology definition served by /api/architecture.
type ArchitectureCatalog struct {
	Title                string             `json:"title"`
	Subtitle             string             `json:"subtitle"`
	Namespace            string             `json:"namespace"`
	Atespace             string             `json:"atespace"`
	SimNamespace         string             `json:"sim_namespace"`
	GeminiModel          string             `json:"gemini_model"`
	DefaultTemplate      string             `json:"default_template"`
	IdleAfter            string             `json:"idle_after"`
	SPIFFEMode           string             `json:"spiffe_mode"`
	SPIFFEDomain         string             `json:"spiffe_domain"`
	SPIFFEPrincipal      string             `json:"spiffe_principal,omitempty"`
	SVIDMounted          bool               `json:"svid_mounted"`
	GovernanceEnforced   bool               `json:"governance_enforced"`
	ModelArmorTemplateID string             `json:"model_armor_template_id,omitempty"`
	Require3LO           bool               `json:"require_3lo"`
	GCPAuthProviderID    string             `json:"gcp_auth_provider_id,omitempty"`
	Zones                []ArchitectureZone `json:"zones"`
	Nodes                []ArchitectureNode `json:"nodes"`
	Edges                []ArchitectureEdge `json:"edges"`
	Flows                []ArchitectureFlow `json:"flows"`
	Modules              []ModuleStage      `json:"modules"`
}

// SearchableText concatenates catalog fields so tests verify parity with GLOSSARY.md.
func (c ArchitectureCatalog) SearchableText() string {
	var b strings.Builder
	b.WriteString(c.Title + "\n" + c.Subtitle + "\n")
	for _, z := range c.Zones {
		b.WriteString(z.Title + " " + z.Subtitle + "\n")
	}
	for _, n := range c.Nodes {
		b.WriteString(n.Title + " " + n.Subtitle + " " + n.Summary + " " + strings.Join(n.Invariants, " ") + "\n")
	}
	for _, f := range c.Flows {
		b.WriteString(f.Title + " " + f.Description + "\n")
		for _, h := range f.Hops {
			b.WriteString(h.Caption + "\n")
		}
	}
	for _, m := range c.Modules {
		b.WriteString(m.Title + " " + m.Summary + "\n")
	}
	return b.String()
}

// BuildArchitectureCatalog returns the authoritative Actor Boundary topology catalog.
func BuildArchitectureCatalog(p CatalogParams) ArchitectureCatalog {
	ns := firstNonEmpty(p.Namespace, "workshop-301")
	as := firstNonEmpty(p.Atespace, ns)
	sim := firstNonEmpty(p.SimNamespace, "prod-sim")
	modelName := firstNonEmpty(p.GeminiModel, "gemini-3.8-flash")
	tmpl := firstNonEmpty(p.DefaultTemplate, "sre-agent-v1")
	idle := p.IdleAfter
	if idle <= 0 {
		idle = 15 * time.Second
	}
	spiffeMode := firstNonEmpty(p.SPIFFEMode, "wif")
	spiffeDom := firstNonEmpty(p.SPIFFEDomain, "svc.id.goog")
	armorTmpl := normalizeOptionalID(p.ModelArmorTemplateID)
	authProvID := normalizeOptionalID(p.GCPAuthProviderID)

	armorLabel := "Model Armor defense-in-depth"
	if armorTmpl != "" {
		armorLabel = "Model Armor template " + armorTmpl
	}
	authLabel := "OAuth 2.0 + S256 PKCE 3LO vault"
	if authProvID != "" {
		authLabel = "Auth Manager (" + authProvID + ")"
	}
	svidInvariant := "Holds Pod Agent Identity / Workload Identity (" + spiffeMode + " · " + spiffeDom + ") outside the sandbox"
	if p.SVIDMounted {
		svidInvariant = "Verified mounted X.509 SVID (" + spiffeMode + " · " + spiffeDom + ") outside the sandbox"
	}

	return ArchitectureCatalog{
		Title:                "Actor boundary & governed broker topology",
		Subtitle:             "Stateful ADK Go v2 Incident Actors inside gVisor paired with Gemini platform outside the Actor Boundary.",
		Namespace:            ns,
		Atespace:             as,
		SimNamespace:         sim,
		GeminiModel:          modelName,
		DefaultTemplate:      tmpl,
		IdleAfter:            idle.String(),
		SPIFFEMode:           spiffeMode,
		SPIFFEDomain:         spiffeDom,
		SPIFFEPrincipal:      p.SPIFFEPrincipal,
		SVIDMounted:          p.SVIDMounted,
		GovernanceEnforced:   p.GovernanceEnforced,
		ModelArmorTemplateID: armorTmpl,
		Require3LO:           p.Require3LO,
		GCPAuthProviderID:    authProvID,
		Zones: []ArchitectureZone{
			{
				ID:       "ingress-broker",
				Title:    "Ingress & governed broker (" + ns + ")",
				Subtitle: "Always-on identity, console & credential injection",
				X:        20,
				Y:        20,
				Width:    320,
				Height:   464,
			},
			{
				ID:       "actor-boundary",
				Title:    "Actor boundary · Agent Substrate (" + as + ")",
				Subtitle: "Keyless gVisor actors with 0 vCPU GCS suspension",
				X:        372,
				Y:        20,
				Width:    324,
				Height:   464,
			},
			{
				ID:       "cloud-and-sim",
				Title:    "Gemini platform & " + sim,
				Subtitle: "Models, Sessions, Memory, Catalogs & Target Service",
				X:        728,
				Y:        20,
				Width:    332,
				Height:   464,
			},
		},
		Nodes: []ArchitectureNode{
			{
				ID:       "operator",
				ZoneID:   "ingress-broker",
				Title:    "SRE operator & Incident Console",
				Subtitle: "Browser UI & curl CLI",
				Badge:    "Client",
				Summary:  "Drives investigations in the Google Cloud Console-modeled Incident Console, inspects zero-wake Session Ledger history, and approves the Remediation Gate.",
				Invariants: []string{
					"Zero OAuth bearer tokens or API keys stored in browser storage",
					"Reads suspended actor workspaces and Session Ledger history without waking compute",
				},
				X:      38,
				Y:      76,
				Width:  284,
				Height: 86,
			},
			{
				ID:       "ingress-gateway",
				ZoneID:   "ingress-broker",
				Title:    "GKE gateways & GCPAgentGateway",
				Subtitle: "sre-ingress-gateway · CLIENT_TO_AGENT",
				Badge:    "Perimeter",
				Summary:  "Declarative external Gateway/sre-ingress-gateway (HTTPRoute + L7 GCPAuthzPolicy) paired with internal Gateway/sre-agent-gateway (gke-l7-rilb + GCPAgentGateway) advertising A2A and Agent Registry bindings.",
				Invariants: []string{
					"Provisioned asynchronously during Module 00 setup, with HTTPRoute pre-staged on make gateway",
					"Enforces L7 GCPAuthzPolicy rules (<= 10 paths) on EXTERNAL_MANAGED in Module 05",
				},
				X:      38,
				Y:      190,
				Width:  284,
				Height: 90,
			},
			{
				ID:       "sre-gateway",
				ZoneID:   "ingress-broker",
				Title:    "sre-gateway (Governed Broker)",
				Subtitle: "SPIFFE: " + spiffeDom,
				Badge:    "100m CPU · Always-on",
				Summary:  "The Governed Broker outside the Actor Boundary that serves the Incident Console, dials atenet-router over mTLS, attaches Workload Identity or 3LO tokens, screens prompts with Model Armor, and suspends idle Incident Actors.",
				Invariants: []string{
					svidInvariant,
					"Screens prompts and incident notes via " + armorLabel + " and brokers 3LO via " + authLabel,
					"Routes /genai, /agentengine, /agentregistry, /mcp, /ops, and /otlp; suspends idle actors after " + idle.String(),
				},
				X:      38,
				Y:      308,
				Width:  284,
				Height: 140,
			},
			{
				ID:       "atenet-router",
				ZoneID:   "actor-boundary",
				Title:    "atenet-router & atenet-egress",
				Subtitle: "ate-target-actor mTLS + EgressPolicy",
				Badge:    "Dataplane",
				Summary:  "Authenticates sre-gateway via podCertificate mTLS, parks turns while waking suspended Incident Actors in under 400ms, and enforces default-deny EgressPolicy on outbound traffic.",
				Invariants: []string{
					"Requires spiffe://cluster.local/ns/" + ns + "/sa/sre-gateway on ingress",
					"Restricts Incident Actor egress exclusively to sre-gateway." + ns + ".svc.cluster.local",
				},
				X:      390,
				Y:      76,
				Width:  288,
				Height: 96,
			},
			{
				ID:       "incident-actor",
				ZoneID:   "actor-boundary",
				Title:    "Incident Actor (" + tmpl + ")",
				Subtitle: "ADK Go v2 · /workspace durableDir",
				Badge:    "Keyless gVisor",
				Summary:  "Isolated per-incident ADK Go v2 sandbox (inc-402, inc-409) running on WorkerPool/sre-workers. Clones code into /workspace, executes SHA-256 Procedural Skill runbooks, and pauses at the Remediation Gate.",
				Invariants: []string{
					"Zero ambient credentials in env, mounted volumes, or RAM checkpoints",
					"512 MiB-768 MiB gVisor sandbox with allowlisted workspace tools",
				},
				X:      390,
				Y:      202,
				Width:  288,
				Height: 110,
			},
			{
				ID:       "gcs-snapshots",
				ZoneID:   "actor-boundary",
				Title:    "Golden Snapshot & GCS checkpoints",
				Subtitle: "SNAPSHOT_FIDELITY_MEMORY · 0 vCPU",
				Badge:    "0 vCPU when idle",
				Summary:  "Warms a shared Golden Snapshot per template and checkpoints idle or gated Incident Actors (memory + /workspace durableDir) to Cloud Storage at 0 vCPU and 0 MB RAM.",
				Invariants: []string{
					"Uniform-CPU node pools preserve sub-400ms cross-worker memory restores",
					"Snapshots contain zero bearer tokens or OAuth secrets",
				},
				X:      390,
				Y:      342,
				Width:  288,
				Height: 96,
			},
			{
				ID:       "agent-platform",
				ZoneID:   "cloud-and-sim",
				Title:    "Gemini platform",
				Subtitle: "Models · Sessions · Memory · Catalogs",
				Badge:    modelName,
				Summary:  "Managed enterprise services reached exclusively through sre-gateway: Gemini platform models, Session Ledger (Gemini Sessions), Consolidated Memory (Gemini Memory Bank), Procedural Skill Registry, Agent Registry, Model Armor, Auth Manager, Cloud Trace, Online Evals, and Security Command Center Anomaly Detection.",
				Invariants: []string{
					"Session Ledger & Consolidated Memory survive v1 -> v2 template rollouts",
					"Procedural Skill revisions ship without Golden Snapshot churn",
					"Cloud Trace + Security Command Center Anomaly Detection analyze GKE_WORKLOAD logs and OTel spans out-of-band",
				},
				X:      746,
				Y:      76,
				Width:  296,
				Height: 190,
			},
			{
				ID:       "prod-sim",
				ZoneID:   "cloud-and-sim",
				Title:    "Target Service & Operations Hub",
				Subtitle: "ledger-api + ops-hub (" + sim + ")",
				Badge:    "Live incident sim",
				Summary:  "Real degraded Target Service (ledger-api exhibiting connection-pool exhaustion) paired with the self-contained Operations Hub (ledger-api.git, INC-402, prompt-injected INC-409, MCP server, and 3LO OAuth 2.0 provider).",
				Invariants: []string{
					"Zero external SaaS account signups required in classrooms",
					"Supports optional GitHub OAuth override in workshop.env",
				},
				X:      746,
				Y:      302,
				Width:  296,
				Height: 136,
			},
		},
		Edges: []ArchitectureEdge{
			{
				ID:          "op-to-gw",
				From:        "operator",
				To:          "ingress-gateway",
				Label:       "HTTP / Incident Console",
				Protocol:    "HTTP",
				CrossesGate: false,
			},
			{
				ID:          "gw-to-broker",
				From:        "ingress-gateway",
				To:          "sre-gateway",
				Label:       "CLIENT_TO_AGENT route",
				Protocol:    "HTTP",
				CrossesGate: false,
			},
			{
				ID:          "broker-to-router",
				From:        "sre-gateway",
				To:          "atenet-router",
				Label:       "mTLS · ate-target-actor",
				Protocol:    "HTTPS/2 mTLS",
				CrossesGate: true,
			},
			{
				ID:          "router-to-actor",
				From:        "atenet-router",
				To:          "incident-actor",
				Label:       "Wake & POST /turn (:80)",
				Protocol:    "atunnel",
				CrossesGate: false,
			},
			{
				ID:          "actor-to-broker",
				From:        "incident-actor",
				To:          "sre-gateway",
				Label:       "Keyless /genai, /agentengine, /agentregistry, /mcp & /ops",
				Protocol:    "EgressPolicy HTTP",
				CrossesGate: true,
			},
			{
				ID:          "actor-to-gcs",
				From:        "incident-actor",
				To:          "gcs-snapshots",
				Label:       "Checkpoint / <400ms restore",
				Protocol:    "SNAPSHOT_FIDELITY_MEMORY",
				CrossesGate: false,
			},
			{
				ID:          "broker-to-platform",
				From:        "sre-gateway",
				To:          "agent-platform",
				Label:       "WIF · Gemini platform",
				Protocol:    "HTTPS + Workload Identity",
				CrossesGate: false,
			},
			{
				ID:          "broker-to-sim",
				From:        "sre-gateway",
				To:          "prod-sim",
				Label:       "Git clone & 3LO remediation",
				Protocol:    "HTTP + 3LO Bearer",
				CrossesGate: false,
			},
		},
		Flows: []ArchitectureFlow{
			{
				ID:          "turn-1-triage",
				Title:       "Turn 1 keyless incident triage",
				Description: "Operator dispatches INC-402; sre-gateway wakes inc-402 from the Golden Snapshot, clones ledger-api via /ops, and calls Gemini platform with zero sandbox credentials.",
				Hops: []ArchitectureHop{
					{Step: 1, EdgeID: "op-to-gw", Caption: "Operator submits INC-402 triage turn in Incident Console", Tone: "primary"},
					{Step: 2, EdgeID: "gw-to-broker", Caption: "Gateway forwards turn to sre-gateway Governed Broker", Tone: "primary"},
					{Step: 3, EdgeID: "broker-to-router", Caption: "sre-gateway ensures actor + EgressPolicy and dials atenet-router over mTLS", Tone: "primary"},
					{Step: 4, EdgeID: "router-to-actor", Caption: "atenet-router wakes keyless Incident Actor inc-402 on WorkerPool/sre-workers", Tone: "success"},
					{Step: 5, EdgeID: "actor-to-broker", Caption: "ADK Go v2 Runner calls /ops/git and /genai across strict EgressPolicy", Tone: "primary"},
					{Step: 6, EdgeID: "broker-to-sim", Caption: "sre-gateway streams ledger-api.git from Operations Hub into /workspace", Tone: "primary"},
					{Step: 7, EdgeID: "broker-to-platform", Caption: "sre-gateway attaches Workload Identity and calls Gemini platform", Tone: "success"},
				},
			},
			{
				ID:          "idle-and-gate-suspend",
				Title:       "Idle & Remediation Gate suspension (0 vCPU)",
				Description: "After " + idle.String() + " idle or at an ADK Remediation Gate interrupt, sre-gateway suspends the Incident Actor to Cloud Storage while Session Ledger remains readable.",
				Hops: []ArchitectureHop{
					{Step: 1, EdgeID: "actor-to-gcs", Caption: "Incident Actor memory + /workspace durableDir checkpointed to Cloud Storage (0 vCPU)", Tone: "warning"},
					{Step: 2, EdgeID: "op-to-gw", Caption: "Operator inspects workspace diff and Session Ledger with zero compute wake", Tone: "primary"},
					{Step: 3, EdgeID: "broker-to-router", Caption: "On follow-up or approval, atenet-router restores inc-402 in <400ms", Tone: "success"},
					{Step: 4, EdgeID: "router-to-actor", Caption: "Resumed Incident Actor continues with intact /workspace git branch", Tone: "success"},
				},
			},
			{
				ID:          "skill-hot-reload",
				Title:       "Zero-rebuild Procedural Skill rollout (v1 -> v2)",
				Description: "Publishing revision v2 to Skill Registry updates defaultRevision; the resumed Incident Actor verifies SHA-256 and unpacks v2 into /workspace/.skills/ with zero Golden Snapshot churn.",
				Hops: []ArchitectureHop{
					{Step: 1, EdgeID: "op-to-gw", Caption: "Operator publishes ledger-pool-triage@v2 and flips defaultRevision to v2", Tone: "primary"},
					{Step: 2, EdgeID: "broker-to-platform", Caption: "Skill Registry stores immutable ZIP archive and SHA-256 digest", Tone: "primary"},
					{Step: 3, EdgeID: "router-to-actor", Caption: "atenet-router wakes existing sre-agent-v3 Incident Actor without rebuilding image", Tone: "success"},
					{Step: 4, EdgeID: "actor-to-broker", Caption: "RegistrySkillSource pulls /agentregistry/skills/ledger-pool-triage@v2 and verifies SHA-256", Tone: "success"},
				},
			},
			{
				ID:          "governance-defense",
				Title:       "Zero-trust governance & prompt-injection block",
				Description: "Inbound GCPAuthzPolicy, inline Model Armor screening on INC-409, MCP readOnlyHint/destructiveHint checks, and 3LO Auth Manager token injection.",
				Hops: []ArchitectureHop{
					{Step: 1, EdgeID: "gw-to-broker", Caption: "GCPAgentGateway + GCPAuthzPolicy validate inbound request posture", Tone: "primary"},
					{Step: 2, EdgeID: "actor-to-broker", Caption: "Poisoned INC-409 note or unapproved MCP call reaches Governed Broker", Tone: "warning"},
					{Step: 3, EdgeID: "broker-to-platform", Caption: "Model Armor & Agent Registry policy block payload before execution (HTTP 403)", Tone: "error"},
				},
			},
			{
				ID:          "otel-evals-and-scc",
				Title:       "OpenTelemetry GenAI waterfall, online evals & SCC",
				Description: "W3C traceparent propagates across the Actor Boundary; keyless /otlp/v1/traces scrubs secrets before Cloud Trace export, Gemini autoraters score 4 rubrics, and Security Command Center Anomaly Detection monitors GKE_WORKLOAD logs out-of-band.",
				Hops: []ArchitectureHop{
					{Step: 1, EdgeID: "broker-to-router", Caption: "sre-gateway propagates W3C traceparent into atenet-router and keyless Incident Actor", Tone: "primary"},
					{Step: 2, EdgeID: "actor-to-broker", Caption: "ADK Go v2 posts invoke_agent & execute_tool spans to /otlp/v1/traces across EgressPolicy", Tone: "primary"},
					{Step: 3, EdgeID: "broker-to-platform", Caption: "Broker scrubs tokens/payloads, exports gen_ai.* waterfall to Cloud Trace, and runs 4-rubric Gemini online evals + SCC Anomaly Detection", Tone: "success"},
				},
			},
		},
		Modules: []ModuleStage{
			{Number: "00", Title: "Setup & live prod-sim", Summary: "Uniform-CPU preflight, quota-safe ComputeClass, Substrate v1.0.0, and degraded ledger-api + ops-hub."},
			{Number: "01", Title: "Actor boundary & first triage turn", Summary: "Deploy sre-gateway + Incident Console, wire keyless ADK Go v2 sre-agent-v1, diagnose INC-402, and idle-suspend to 0 vCPU."},
			{Number: "02", Title: "Zero-wake Session Ledger & Consolidated Memory", Summary: "Persist turns to Gemini Sessions, audit at 0 vCPU, and prove cross-rollout recall via Gemini Memory Bank."},
			{Number: "03", Title: "Dynamic Procedural Skills via Skill Registry", Summary: "Pull SHA-256-verified SKILL.md + diagnostic shell scripts at runtime with zero Golden Snapshot churn."},
			{Number: "04", Title: "Agent Registry discovery & Remediation Gate", Summary: "Advertise A2A Agent Card, discover MCP servers, and suspend to 0 vCPU at ADK toolconfirmation."},
			{Number: "05", Title: "Zero-trust governance", Summary: "Attach GCPAuthzPolicy, Pod Agent Identity SPIFFE, 3LO Auth Manager, Model Armor block on INC-409, and snapshot token audit."},
			{Number: "06", Title: "Observability & pure-Go online evals", Summary: "Export scrubbed W3C GenAI OTel spans to Cloud Trace and score sessions with Gemini autorater rubrics."},
			{Number: "07", Title: "Optional Ambient egress & wrap-up", Summary: "Explore optional Ambient Mesh + Secure Web Proxy overlay and clean up workshop resources."},
		},
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
