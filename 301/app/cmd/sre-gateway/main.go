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

// Command sre-gateway runs the always-on Governed Broker outside the Actor Boundary.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gke-demos/substrate-agent-platform/app/internal/ateclient"
	"github.com/gke-demos/substrate-agent-platform/app/internal/broker"
)

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("sre-gateway config: %v", err)
	}

	srv, err := broker.NewServer(cfg)
	if err != nil {
		log.Fatalf("sre-gateway init: %v", err)
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	log.Printf(
		"sre-gateway listening on %s (atespace=%s template=%s mock_llm=%t idle_after=%s spiffe=%s)",
		cfg.ListenAddr, cfg.Atespace, cfg.DefaultTemplate, cfg.MockLLM, cfg.IdleAfter, cfg.SPIFFEDomain,
	)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("sre-gateway serve: %v", err)
	}
}

func loadConfig(getenv func(string) string) (broker.Config, error) {
	or := func(k, fallback string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return fallback
	}
	var errs []error
	parseBool := func(k, fallback string) bool {
		raw := or(k, fallback)
		b, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s=%q is not a boolean", k, raw))
		}
		return b
	}
	parseDur := func(k, fallback string) time.Duration {
		raw := or(k, fallback)
		d, err := time.ParseDuration(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s=%q is not a valid duration", k, raw))
		}
		return d
	}

	ns := or("NAMESPACE", "workshop-301")
	simNS := or("SIM_NAMESPACE", "prod-sim")
	port := or("PORT", "8080")
	listenAddr := or("LISTEN_ADDR", ":"+port)

	oauthSecret := strings.TrimSpace(getenv("OAUTH_CLIENT_SECRET"))
	if secretFile := strings.TrimSpace(getenv("OAUTH_CLIENT_SECRET_FILE")); oauthSecret == "" && secretFile != "" {
		if raw, err := os.ReadFile(secretFile); err == nil {
			oauthSecret = strings.TrimSpace(string(raw))
		}
	}

	cfg := broker.Config{
		ListenAddr:               listenAddr,
		Namespace:                ns,
		Atespace:                 or("ATESPACE", ns),
		SimNamespace:             simNS,
		ProjectID:                strings.TrimSpace(getenv("GOOGLE_CLOUD_PROJECT")),
		Region:                   or("REGION", "us-central1"),
		ClusterName:              strings.TrimSpace(getenv("CLUSTER_NAME")),
		ClusterLocation:          strings.TrimSpace(getenv("CLUSTER_LOCATION")),
		ModelLocation:            or("GOOGLE_CLOUD_LOCATION", or("MODEL_LOCATION", "global")),
		GeminiModel:              or("GEMINI_MODEL", "gemini-3.8-flash"),
		MockLLM:                  parseBool("MOCK_LLM", "false"),
		ReasoningEngineID:        strings.TrimSpace(getenv("REASONING_ENGINE_ID")),
		DefaultTemplate:          or("DEFAULT_ACTOR_TEMPLATE", "sre-agent-v1"),
		SelfURL:                  or("SELF_URL", fmt.Sprintf("http://sre-gateway.%s.svc.cluster.local:8080", ns)),
		RouterURL:                or("ROUTER_URL", "https://atenet-router.ate-system.svc.cluster.local"),
		RouterServerName:         or("ROUTER_SERVER_NAME", "atenet-router.ate-system.svc"),
		RouterCAFile:             or("ROUTER_CA_FILE", "/var/run/ate/trust/trust-bundle.pem"),
		RouterClientCert:         or("ROUTER_CLIENT_CERT_FILE", "/var/run/ate/identity/credential-bundle.pem"),
		RouterInsecure:           parseBool("ROUTER_INSECURE", "false"),
		OpsHubURL:                or("OPS_HUB_URL", fmt.Sprintf("http://ops-hub.%s.svc.cluster.local:8080", simNS)),
		LedgerAPIURL:             or("LEDGER_API_URL", fmt.Sprintf("http://ledger-api.%s.svc.cluster.local:8080", simNS)),
		SkillRegistryLocation:    or("SKILL_REGISTRY_LOCATION", "global"),
		SkillRegistryUpstreamURL: strings.TrimSpace(getenv("SKILL_REGISTRY_UPSTREAM_URL")),
		ModelArmorUpstreamURL:    strings.TrimSpace(getenv("MODEL_ARMOR_UPSTREAM_URL")),
		ModelArmorTemplateID:     strings.TrimSpace(getenv("MODEL_ARMOR_TEMPLATE_ID")),
		GovernanceEnforced:       parseBool("GOVERNANCE_ENFORCED", "false"),
		Require3LO:               parseBool("REQUIRE_3LO_FOR_REMEDIATION", "false"),
		GCPAuthProviderID:        strings.TrimSpace(getenv("GCP_AUTH_PROVIDER_ID")),
		AuthIdentityUpstreamURL:  strings.TrimSpace(getenv("AUTH_IDENTITY_UPSTREAM_URL")),
		OAuthClientID:            or("OAUTH_CLIENT_ID", "sre-gateway-3lo"),
		OAuthClientSecret:        oauthSecret,
		GitHubOAuthClientID:      strings.TrimSpace(getenv("GITHUB_OAUTH_CLIENT_ID")),
		GitHubOAuthClientSecret:  strings.TrimSpace(getenv("GITHUB_OAUTH_CLIENT_SECRET")),
		CloudTraceUpstreamURL:    strings.TrimSpace(getenv("CLOUD_TRACE_UPSTREAM_URL")),
		StateFile:                strings.TrimSpace(getenv("BROKER_STATE_FILE")),
		AdminToken:               strings.TrimSpace(getenv("BROKER_ADMIN_TOKEN")),
		IdleAfter:                parseDur("IDLE_AFTER", "15s"),
		IdleCheckEvery:           parseDur("IDLE_CHECK_EVERY", "2s"),
		SPIFFEMode:               or("AGENT_IDENTITY_MODE", "wif"),
		SPIFFEDomain:             strings.TrimSpace(getenv("SPIFFE_TRUST_DOMAIN")),
		AgentIdentityCertFile:    or("AGENT_IDENTITY_CERT_FILE", broker.DefaultAgentIdentityCertFile),
		ATE: ateclient.Options{
			Addr:      or("ATE_API_ADDR", "api.ate-system.svc.cluster.local:443"),
			TokenFile: or("ATE_API_TOKEN_FILE", "/var/run/ate/token/token"),
			CAFile:    or("ATE_API_CA_FILE", "/var/run/ate/trust/trust-bundle.pem"),
			Insecure:  parseBool("ATE_API_INSECURE", "false"),
		},
	}
	if !cfg.MockLLM && strings.TrimSpace(getenv("GOOGLE_GENAI_USE_ENTERPRISE")) != "true" {
		errs = append(errs, errors.New("GOOGLE_GENAI_USE_ENTERPRISE=true is required when MOCK_LLM=false"))
	}
	return cfg, errors.Join(errs...)
}
