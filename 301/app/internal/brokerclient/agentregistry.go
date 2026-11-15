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
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gke-demos/substrate-agent-platform/app/internal/telemetry"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agentregistry"
	"google.golang.org/adk/v2/tool"
)

// NewAgentRegistryClient constructs an upstream ADK Go v2 agentregistry.Client
// whose HTTP transport routes agentregistry.googleapis.com/v1 requests across
// the keyless Actor Boundary to <brokerURL>/agentregistry/v1.
func NewAgentRegistryClient(ctx context.Context, brokerURL, projectID, location string, hc *http.Client) (*agentregistry.Client, error) {
	base := strings.TrimRight(strings.TrimSpace(brokerURL), "/")
	if base == "" {
		return nil, errors.New("brokerclient: BROKER_URL is required")
	}
	parsedBroker, err := url.Parse(base)
	if err != nil || parsedBroker.Host == "" {
		return nil, fmt.Errorf("brokerclient: invalid BROKER_URL %q", brokerURL)
	}

	proj := strings.TrimSpace(projectID)
	if proj == "" {
		proj = strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT"))
	}
	if proj == "" {
		proj = "workshop-project"
	}

	loc := strings.TrimSpace(location)
	if loc == "" {
		loc = strings.TrimSpace(os.Getenv("REGION"))
	}
	if loc == "" {
		loc = "us-central1"
	}

	var baseTransport http.RoundTripper = http.DefaultTransport
	timeout := 30 * time.Second
	if hc != nil {
		if hc.Transport != nil {
			baseTransport = hc.Transport
		}
		if hc.Timeout > 0 {
			timeout = hc.Timeout
		}
	}

	rewrittenHC := telemetry.WrapHTTPClient(&http.Client{
		Transport: &agentRegistryBrokerTransport{
			base:   baseTransport,
			broker: parsedBroker,
		},
		Timeout: timeout,
	}, timeout)

	return agentregistry.New(ctx, agentregistry.Config{
		ProjectID:  proj,
		Location:   loc,
		HTTPClient: rewrittenHC,
	})
}

type agentRegistryBrokerTransport struct {
	base   http.RoundTripper
	broker *url.URL
}

func (t *agentRegistryBrokerTransport) CloseIdleConnections() {
	if ic, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		ic.CloseIdleConnections()
	}
}

func (t *agentRegistryBrokerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	host := strings.ToLower(cloned.URL.Hostname())
	if host == "agentregistry.googleapis.com" || host == "agentregistry.mtls.googleapis.com" {
		cloned.URL.Scheme = t.broker.Scheme
		cloned.URL.Host = t.broker.Host
		cloned.Host = t.broker.Host
		basePath := strings.TrimRight(t.broker.Path, "/")
		cloned.URL.Path = basePath + "/agentregistry" + cloned.URL.Path
		cloned.URL.RawPath = ""
	}
	return t.base.RoundTrip(cloned)
}

// DiscoverMCPToolsets lists governed MCP servers from regClient and connects an
// ADK Go v2 MCPToolset for each server routed via brokerURL. On ops-hub-mcp,
// apply_remediation_pr is filtered out of the raw MCP toolset so the explicit
// confirmation-gated functiontool (NewRemediationTools) owns that tool name.
func DiscoverMCPToolsets(ctx context.Context, regClient *agentregistry.Client, brokerURL string, hc *http.Client) ([]tool.Toolset, error) {
	if regClient == nil {
		return nil, errors.New("brokerclient: agentregistry.Client is nil")
	}
	base := strings.TrimRight(strings.TrimSpace(brokerURL), "/")
	if base == "" {
		return nil, errors.New("brokerclient: BROKER_URL is required")
	}

	mcpHC := buildStreamingSafeHTTPClient(hc)

	var toolsets []tool.Toolset
	for srv, err := range regClient.AllMCPServers(ctx) {
		if err != nil {
			closeToolsets(toolsets)
			return nil, fmt.Errorf("brokerclient: list MCP servers from Agent Registry: %w", err)
		}
		if srv == nil || !hasGovernedBrokerInterface(srv, base) {
			continue
		}
		ts, err := regClient.MCPToolset(ctx, srv.Name, agentregistry.WithMCPHTTPClient(mcpHC))
		if err != nil {
			closeToolsets(toolsets)
			return nil, fmt.Errorf("brokerclient: connect MCPToolset %s: %w", srv.Name, err)
		}
		if srv.MCPServerID == "ops-hub-mcp" || strings.HasSuffix(srv.Name, "/ops-hub-mcp") {
			ts = wrapClosableFilteredToolset(ts, func(_ agent.ReadonlyContext, tl tool.Tool) bool {
				return tl.Name() != "apply_remediation_pr"
			})
		}
		toolsets = append(toolsets, ts)
	}
	return toolsets, nil
}

func hasGovernedBrokerInterface(srv *agentregistry.MCPServer, brokerBase string) bool {
	prefix := brokerBase + "/mcp/"
	for _, iface := range srv.Interfaces {
		if strings.HasPrefix(iface.URL, prefix) {
			return true
		}
	}
	for _, p := range srv.Protocols {
		for _, iface := range p.Interfaces {
			if strings.HasPrefix(iface.URL, prefix) {
				return true
			}
		}
	}
	return false
}

func buildStreamingSafeHTTPClient(hc *http.Client) *http.Client {
	if hc != nil {
		// Copy without whole-request Timeout so Streamable HTTP MCP sessions do not get truncated.
		cp := *hc
		cp.Timeout = 0
		return telemetry.WrapHTTPClient(&cp, 0)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 30 * time.Second
	return telemetry.WrapHTTPClient(&http.Client{Transport: tr}, 0)
}

type closableFilteredToolset struct {
	tool.Toolset
	closer io.Closer
}

func wrapClosableFilteredToolset(raw tool.Toolset, pred tool.Predicate) tool.Toolset {
	filtered := tool.FilterToolset(raw, pred)
	if c, ok := raw.(io.Closer); ok {
		return &closableFilteredToolset{Toolset: filtered, closer: c}
	}
	return filtered
}

func (c *closableFilteredToolset) Close() error {
	if c.closer != nil {
		return c.closer.Close()
	}
	return nil
}

func closeToolsets(list []tool.Toolset) {
	for _, ts := range list {
		if c, ok := ts.(io.Closer); ok {
			_ = c.Close()
		}
	}
}
