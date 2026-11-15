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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/httptransport"
)

// RegistryToolAnnotationsDTO serializes explicit MCP behavioral hints without omitempty
// on readOnlyHint and destructiveHint so clients and UI tables see unambiguous values.
type RegistryToolAnnotationsDTO struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

func defaultRegistryToolAnnotations() RegistryToolAnnotationsDTO {
	// Per google.golang.org/adk/v2/agentregistry/types.go, absent hints carry API-side
	// defaults of DestructiveHint=true and OpenWorldHint=true.
	return RegistryToolAnnotationsDTO{
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  false,
		OpenWorldHint:   true,
	}
}

// UnmarshalJSON applies ADK Go v2 Agent Registry defaults when destructiveHint or
// openWorldHint are omitted from JSON payloads.
func (a *RegistryToolAnnotationsDTO) UnmarshalJSON(data []byte) error {
	if string(bytes.TrimSpace(data)) == "null" {
		*a = defaultRegistryToolAnnotations()
		return nil
	}
	var raw struct {
		Title           string `json:"title"`
		ReadOnlyHint    *bool  `json:"readOnlyHint"`
		DestructiveHint *bool  `json:"destructiveHint"`
		IdempotentHint  *bool  `json:"idempotentHint"`
		OpenWorldHint   *bool  `json:"openWorldHint"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	a.Title = raw.Title
	if raw.ReadOnlyHint != nil {
		a.ReadOnlyHint = *raw.ReadOnlyHint
	}
	if raw.IdempotentHint != nil {
		a.IdempotentHint = *raw.IdempotentHint
	}
	if raw.DestructiveHint != nil {
		a.DestructiveHint = *raw.DestructiveHint
	} else {
		a.DestructiveHint = !a.ReadOnlyHint
	}
	if raw.OpenWorldHint != nil {
		a.OpenWorldHint = *raw.OpenWorldHint
	} else {
		a.OpenWorldHint = true
	}
	return nil
}

// RegistryToolDTO describes one tool declared on an Agent Registry MCPServer.
type RegistryToolDTO struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	Annotations RegistryToolAnnotationsDTO `json:"annotations"`
}

// UnmarshalJSON defaults absent tool annotations to DestructiveHint=true and
// OpenWorldHint=true per google.golang.org/adk/v2/agentregistry/types.go.
func (t *RegistryToolDTO) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name        string                      `json:"name"`
		Description string                      `json:"description"`
		Annotations *RegistryToolAnnotationsDTO `json:"annotations"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	t.Name = raw.Name
	t.Description = raw.Description
	if raw.Annotations != nil {
		t.Annotations = *raw.Annotations
	} else {
		t.Annotations = defaultRegistryToolAnnotations()
	}
	return nil
}

// RegistryInterfaceDTO describes a connection endpoint and protocol binding.
type RegistryInterfaceDTO struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
}

// RegistryProtocolDTO describes a protocol entry on an Agent Registry resource.
type RegistryProtocolDTO struct {
	Type            string                 `json:"type,omitempty"`
	ProtocolVersion string                 `json:"protocolVersion,omitempty"`
	Interfaces      []RegistryInterfaceDTO `json:"interfaces,omitempty"`
}

// RegistryMCPServerDTO matches the wire format decoded by google.golang.org/adk/v2/agentregistry.
type RegistryMCPServerDTO struct {
	Name        string                 `json:"name"`
	MCPServerID string                 `json:"mcpServerId"`
	DisplayName string                 `json:"displayName"`
	Description string                 `json:"description"`
	Source      string                 `json:"source,omitempty"`
	Protocols   []RegistryProtocolDTO  `json:"protocols,omitempty"`
	Interfaces  []RegistryInterfaceDTO `json:"interfaces"`
	Tools       []RegistryToolDTO      `json:"tools"`

	upstreamURL string
}

// RegistryCardDTO embeds an A2A Agent Card inside an Agent Registry Agent descriptor.
type RegistryCardDTO struct {
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
}

// RegistryAgentDTO matches the wire format decoded by google.golang.org/adk/v2/agentregistry.
type RegistryAgentDTO struct {
	Name        string                `json:"name"`
	DisplayName string                `json:"displayName"`
	Description string                `json:"description"`
	Version     string                `json:"version"`
	Source      string                `json:"source,omitempty"`
	Protocols   []RegistryProtocolDTO `json:"protocols"`
	Skills      []A2ASkillDTO         `json:"skills,omitempty"`
	Card        *RegistryCardDTO      `json:"card,omitempty"`
}

type agentRegistryProxy struct {
	server       *Server
	mockLLM      bool
	projectID    string
	region       string
	upstreamBase string
	authedClient *http.Client
	recordPolicy func(PolicyEvent)
}

func newAgentRegistryProxy(ctx context.Context, srv *Server, cfg Config, record func(PolicyEvent)) (*agentRegistryProxy, error) {
	proj := firstNonEmpty(cfg.ProjectID, "local-dev")
	region := firstNonEmpty(cfg.Region, "us-central1")
	upBase := strings.TrimRight(firstNonEmpty(cfg.AgentRegistryUpstreamURL, cfg.SkillRegistryUpstreamURL), "/")
	p := &agentRegistryProxy{
		server:       srv,
		mockLLM:      cfg.MockLLM,
		projectID:    proj,
		region:       region,
		upstreamBase: upBase,
		recordPolicy: record,
	}
	if !p.mockLLM {
		if p.upstreamBase != "" {
			p.authedClient = &http.Client{Timeout: 15 * time.Second}
		} else if strings.TrimSpace(cfg.ProjectID) != "" {
			if creds, err := credentials.DetectDefault(&credentials.DetectOptions{
				Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
			}); err == nil {
				if hc, err := httptransport.NewClient(&httptransport.Options{
					Credentials: creds,
				}); err == nil {
					hc.Timeout = 15 * time.Second
					p.authedClient = hc
				}
			}
		}
	}
	_ = ctx
	return p, nil
}

func (p *agentRegistryProxy) cloudEnabled() bool {
	return !p.mockLLM && p.authedClient != nil && p.projectID != "" && p.projectID != "local-dev"
}

func (p *agentRegistryProxy) cloudBaseURL() string {
	if p.upstreamBase != "" {
		return p.upstreamBase
	}
	return "https://agentregistry.googleapis.com"
}

// ServeHTTP handles /agentregistry/v1/projects/{p}/locations/{l}/{mcpServers,agents,endpoints,skills}...
func (p *agentRegistryProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	proj, loc, kind, itemID, rest, ok := parseAgentRegistryV1Path(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if kind == "skills" {
		p.server.skillRegistry.ServeHTTP(w, r)
		return
	}
	if rest != "" {
		http.NotFound(w, r)
		return
	}

	baseURL := p.server.resolveBaseURL(r)

	switch kind {
	case "mcpServers":
		servers := p.listMCPServers(r.Context(), proj, loc, baseURL)
		if itemID == "" {
			p.emitPolicy("ListMCPServers", "ALLOW",
				fmt.Sprintf("Returned %d governed MCP server(s) pointing to %s/mcp/*", len(servers), baseURL))
			writeBrokerJSON(w, http.StatusOK, map[string]any{
				"mcpServers": servers,
			})
			return
		}
		for _, srv := range servers {
			if srv.MCPServerID == itemID || strings.HasSuffix(srv.Name, "/mcpServers/"+itemID) {
				p.emitPolicy("GetMCPServer ("+itemID+")", "ALLOW",
					fmt.Sprintf("Resolved governed MCP endpoint %s", firstInterfaceURL(srv.Interfaces)))
				writeBrokerJSON(w, http.StatusOK, srv)
				return
			}
		}
		writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("mcpServer %q not found", itemID)})

	case "agents":
		agents := p.listAgents(r.Context(), proj, loc, baseURL)
		if itemID == "" {
			p.emitPolicy("ListAgents", "ALLOW",
				fmt.Sprintf("Returned %d registered A2A agent(s)", len(agents)))
			writeBrokerJSON(w, http.StatusOK, map[string]any{
				"agents": agents,
			})
			return
		}
		for _, ag := range agents {
			if strings.HasSuffix(ag.Name, "/agents/"+itemID) || ag.Name == itemID {
				p.emitPolicy("GetAgent ("+itemID+")", "ALLOW", "Resolved A2A Agent Card metadata")
				writeBrokerJSON(w, http.StatusOK, ag)
				return
			}
		}
		writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("agent %q not found", itemID)})

	case "endpoints":
		if itemID != "" {
			writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("endpoint %q not found", itemID)})
			return
		}
		writeBrokerJSON(w, http.StatusOK, map[string]any{"endpoints": []any{}})

	default:
		http.NotFound(w, r)
	}
}

func parseAgentRegistryV1Path(urlPath string) (project, location, kind, itemID, rest string, ok bool) {
	trimmed := strings.Trim(strings.TrimPrefix(urlPath, "/agentregistry/"), "/")
	parts := strings.Split(trimmed, "/")
	// Expected: v1 / projects / {project} / locations / {location} / {kind} [/ {itemID} [/ ...]]
	if len(parts) < 6 || parts[0] != "v1" || parts[1] != "projects" || parts[3] != "locations" {
		return "", "", "", "", "", false
	}
	project = parts[2]
	location = parts[4]
	kind = parts[5]
	if project == "" || location == "" || kind == "" {
		return "", "", "", "", "", false
	}
	if len(parts) >= 7 {
		itemID = parts[6]
	}
	if len(parts) >= 8 {
		rest = strings.Join(parts[7:], "/")
	}
	return project, location, kind, itemID, rest, true
}

func (p *agentRegistryProxy) listMCPServers(ctx context.Context, proj, loc, baseURL string) []RegistryMCPServerDTO {
	proj = firstNonEmpty(proj, p.projectID, "local-dev")
	loc = firstNonEmpty(loc, p.region, "us-central1")
	base := strings.TrimRight(baseURL, "/")

	governed := governedMCPServersCatalog(proj, loc, base)
	byID := make(map[string]int, len(governed))
	for i, s := range governed {
		byID[s.MCPServerID] = i
	}

	if p.cloudEnabled() {
		cloudServers := p.fetchCloudMCPServers(ctx, proj, loc)
		for _, cs := range cloudServers {
			id := firstNonEmpty(cs.MCPServerID, tailAfter(cs.Name, "/mcpServers/"))
			if id == "" {
				continue
			}
			cs.MCPServerID = id
			cs.Source = "agent-registry-cloud"
			if len(cs.Interfaces) == 0 && len(cs.Protocols) > 0 && len(cs.Protocols[0].Interfaces) > 0 {
				cs.Interfaces = cs.Protocols[0].Interfaces
			}
			cs.upstreamURL = firstInterfaceURL(cs.Interfaces)
			if idx, exists := byID[id]; exists {
				// Keep governed /mcp/{id} broker URL while merging any newly registered tools and annotations.
				governed[idx].Source = "agent-registry-cloud+governed-broker"
				governed[idx].Tools = mergeRegistryTools(governed[idx].Tools, cs.Tools)
				continue
			}
			governed = append(governed, cs)
			byID[id] = len(governed) - 1
		}
	}
	return governed
}

func (p *agentRegistryProxy) listAgents(ctx context.Context, proj, loc, baseURL string) []RegistryAgentDTO {
	proj = firstNonEmpty(proj, p.projectID, "local-dev")
	loc = firstNonEmpty(loc, p.region, "us-central1")
	base := strings.TrimRight(baseURL, "/")

	card := p.server.buildAgentCard(base)
	cardRaw, _ := json.Marshal(card)
	a2aURL := base + "/a2a"

	selfAgent := RegistryAgentDTO{
		Name:        fmt.Sprintf("projects/%s/locations/%s/agents/%s", proj, loc, a2aAgentID),
		DisplayName: card.Name,
		Description: card.Description,
		Version:     card.Version,
		Source:      "gke-auto-discovery",
		Protocols: []RegistryProtocolDTO{
			{
				Type:            "A2A_AGENT",
				ProtocolVersion: card.ProtocolVersion,
				Interfaces: []RegistryInterfaceDTO{
					{
						URL:             a2aURL,
						ProtocolBinding: "JSONRPC",
					},
				},
			},
		},
		Skills: card.Skills,
		Card: &RegistryCardDTO{
			Type:    "A2A_AGENT_CARD",
			Content: cardRaw,
		},
	}

	out := []RegistryAgentDTO{selfAgent}
	if p.cloudEnabled() {
		for _, ca := range p.fetchCloudAgents(ctx, proj, loc) {
			agID := strings.Trim(tailAfter(ca.Name, "/agents/"), "/")
			if agID == a2aAgentID || strings.EqualFold(strings.TrimSpace(ca.DisplayName), selfAgent.DisplayName) {
				out[0].Source = "agent-registry-cloud+gke-auto-discovery"
				continue
			}
			ca.DisplayName = firstNonEmpty(ca.DisplayName, agID)
			if ca.DisplayName == "" {
				continue
			}
			ca.Source = "agent-registry-cloud"
			out = append(out, ca)
		}
	}
	return out
}

func governedMCPServersCatalog(proj, loc, baseURL string) []RegistryMCPServerDTO {
	mkServer := func(id, display, desc string, tools []RegistryToolDTO) RegistryMCPServerDTO {
		iface := RegistryInterfaceDTO{
			URL:             baseURL + "/mcp/" + id,
			ProtocolBinding: "JSONRPC",
		}
		return RegistryMCPServerDTO{
			Name:        fmt.Sprintf("projects/%s/locations/%s/mcpServers/%s", proj, loc, id),
			MCPServerID: id,
			DisplayName: display,
			Description: desc,
			Source:      "governed-broker",
			Protocols: []RegistryProtocolDTO{
				{
					Type:            "MCP",
					ProtocolVersion: "2025-03-26",
					Interfaces:      []RegistryInterfaceDTO{iface},
				},
			},
			Interfaces: []RegistryInterfaceDTO{iface},
			Tools:      tools,
		}
	}

	return []RegistryMCPServerDTO{
		mkServer(
			"logging-mcp",
			"Google Cloud Logging MCP",
			"Governed observability MCP server querying Cloud Logging error entries and Target Service structured error telemetry.",
			[]RegistryToolDTO{
				{
					Name:        "query_service_error_logs",
					Description: "Query structured ERROR and WARNING log entries for ledger-api in prod-sim.",
					Annotations: RegistryToolAnnotationsDTO{
						Title:           "Query service error logs",
						ReadOnlyHint:    true,
						DestructiveHint: false,
						IdempotentHint:  true,
						OpenWorldHint:   false,
					},
				},
			},
		),
		mkServer(
			"container-mcp",
			"Google Kubernetes Engine MCP",
			"Governed cluster inspection MCP server exposing read-only GKE workload health alongside policy-blocked destructive namespace controls.",
			[]RegistryToolDTO{
				{
					Name:        "get_deployment_health",
					Description: "Inspect live GKE Deployment health, pod readiness, and connection-pool status for ledger-api.",
					Annotations: RegistryToolAnnotationsDTO{
						Title:           "Get deployment health",
						ReadOnlyHint:    true,
						DestructiveHint: false,
						IdempotentHint:  true,
						OpenWorldHint:   false,
					},
				},
				{
					Name:        "delete_production_namespace",
					Description: "Delete an entire Kubernetes namespace and all running workloads inside it.",
					Annotations: RegistryToolAnnotationsDTO{
						Title:           "Delete production namespace",
						ReadOnlyHint:    false,
						DestructiveHint: true,
						IdempotentHint:  false,
						OpenWorldHint:   false,
					},
				},
			},
		),
		mkServer(
			"ops-hub-mcp",
			"Operations Hub Remediation MCP",
			"Self-contained incident ticket, commit history, SLO status, and confirmation-gated remediation PR server in prod-sim.",
			[]RegistryToolDTO{
				{
					Name:        "get_incident",
					Description: "Fetch an incident ticket and diagnostic metadata by incident ID (e.g. INC-402 or INC-409).",
					Annotations: RegistryToolAnnotationsDTO{
						Title:           "Get incident ticket",
						ReadOnlyHint:    true,
						DestructiveHint: false,
						IdempotentHint:  true,
						OpenWorldHint:   false,
					},
				},
				{
					Name:        "list_commits",
					Description: "List recent git commits on a branch in ledger-api.git.",
					Annotations: RegistryToolAnnotationsDTO{
						Title:           "List repository commits",
						ReadOnlyHint:    true,
						DestructiveHint: false,
						IdempotentHint:  true,
						OpenWorldHint:   false,
					},
				},
				{
					Name:        "get_service_slo",
					Description: "Inspect live health, connection pool saturation, and latency SLO compliance for ledger-api.",
					Annotations: RegistryToolAnnotationsDTO{
						Title:           "Get service SLO status",
						ReadOnlyHint:    true,
						DestructiveHint: false,
						IdempotentHint:  true,
						OpenWorldHint:   false,
					},
				},
				{
					Name:        "apply_remediation_pr",
					Description: "Validate and merge a pushed git remediation branch into main and reload live ledger-api configuration.",
					Annotations: RegistryToolAnnotationsDTO{
						Title:           "Apply remediation pull request",
						ReadOnlyHint:    false,
						DestructiveHint: true,
						IdempotentHint:  false,
						OpenWorldHint:   false,
					},
				},
				{
					Name:        "drop_ledger_database",
					Description: "Permanently drop all ledger database tables in prod-sim (emergency purge).",
					Annotations: RegistryToolAnnotationsDTO{
						Title:           "Drop ledger database",
						ReadOnlyHint:    false,
						DestructiveHint: true,
						IdempotentHint:  false,
						OpenWorldHint:   false,
					},
				},
			},
		),
	}
}

func (p *agentRegistryProxy) fetchCloudMCPServers(ctx context.Context, proj, loc string) []RegistryMCPServerDTO {
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	u := fmt.Sprintf("%s/v1/projects/%s/locations/%s/mcpServers", p.cloudBaseURL(), proj, loc)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	resp, err := p.authedClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var out struct {
		MCPServers []RegistryMCPServerDTO `json:"mcpServers"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&out); err != nil {
		return nil
	}
	return out.MCPServers
}

func (p *agentRegistryProxy) fetchCloudAgents(ctx context.Context, proj, loc string) []RegistryAgentDTO {
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	u := fmt.Sprintf("%s/v1/projects/%s/locations/%s/agents", p.cloudBaseURL(), proj, loc)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	resp, err := p.authedClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var out struct {
		Agents []RegistryAgentDTO `json:"agents"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&out); err != nil {
		return nil
	}
	return out.Agents
}

func (p *agentRegistryProxy) emitPolicy(action, verdict, detail string) {
	if p.recordPolicy == nil {
		return
	}
	p.recordPolicy(PolicyEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Boundary:  "Agent Registry (/agentregistry)",
		Action:    action,
		Verdict:   verdict,
		Detail:    detail,
	})
}

func (p *agentRegistryProxy) lookupMCPServer(ctx context.Context, serverID, baseURL string) (RegistryMCPServerDTO, bool) {
	id := strings.TrimSpace(serverID)
	if id == "" || p == nil {
		return RegistryMCPServerDTO{}, false
	}
	for _, srv := range p.listMCPServers(ctx, p.projectID, p.region, baseURL) {
		if srv.MCPServerID == id || strings.HasSuffix(srv.Name, "/mcpServers/"+id) {
			return srv, true
		}
	}
	return RegistryMCPServerDTO{}, false
}

func findMCPTool(srv RegistryMCPServerDTO, toolName string) (RegistryToolDTO, bool) {
	want := strings.TrimSpace(toolName)
	for _, tl := range srv.Tools {
		if tl.Name == want {
			return tl, true
		}
	}
	return RegistryToolDTO{}, false
}

func mergeRegistryTools(baseTools, extraTools []RegistryToolDTO) []RegistryToolDTO {
	if len(extraTools) == 0 {
		return baseTools
	}
	out := append([]RegistryToolDTO(nil), baseTools...)
	indexByName := make(map[string]int, len(out))
	for i, tl := range out {
		indexByName[tl.Name] = i
	}
	for _, tl := range extraTools {
		if strings.TrimSpace(tl.Name) == "" {
			continue
		}
		if idx, exists := indexByName[tl.Name]; exists {
			out[idx] = tl
			continue
		}
		out = append(out, tl)
		indexByName[tl.Name] = len(out) - 1
	}
	return out
}

func firstInterfaceURL(ifaces []RegistryInterfaceDTO) string {
	for _, i := range ifaces {
		if i.URL != "" {
			return i.URL
		}
	}
	return ""
}
