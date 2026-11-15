# 2. Self-Contained In-Cluster Incident and OAuth 2.0 Environment (`prod-sim`)

Status: accepted

We deploy a lightweight, real Go target microservice (`ledger-api`) and an in-cluster Git, Incident Ticket, MCP, and OAuth 2.0 server (`ops-hub`) in namespace `prod-sim` during `00-setup` so every workshop module runs 100% live against real cluster state without requiring external SaaS account signups.

## Context & Rationale

1. **Zero external SaaS friction in Google Skills & BYO labs**: Attendees in ephemeral Google Skills (Qwiklabs) classrooms or fresh BYO Google Cloud projects cannot create corporate GitHub or Jira OAuth 2.0 applications during a 90-minute lab, yet Module 05 must exercise real **Agent Identity Auth Manager** (`gcloud agent-identity auth-providers` + `adk/v2/auth/gcp`) 3-legged OAuth token brokering. Because Cloud `gcloud agent-identity auth-providers` requires an `https://` OAuth authorization and token endpoint (`GITHUB_OAUTH_CLIENT_ID` or `AUTH_PROVIDER_HTTPS_URL`), `setup-governance.sh` registers a Cloud `AuthProvider` whenever an HTTPS OAuth endpoint is configured, while HTTP-only classroom gateways (`localhost` port-forward or uncertified regional LB IP) automatically use `ops-hub`'s built-in standards-compliant OAuth 2.0 + S256 PKCE provider with short-lived server-side in-memory vault storage.
2. **Real stateful sandbox workload, zero mocked snapshots**: `ledger-api` emits genuine error logs to Cloud Logging and exhibits a real connection-pool/goroutine fault, while `ops-hub` serves a bare `ledger-api.git` repository and incident tickets (`INC-402` clean triage; `INC-409` indirect prompt-injection payload). The Incident Actor clones `ledger-api.git` into `/workspace`, runs real diagnostic scripts and `go test` inside gVisor, stages a real git commit, and pushes the approved remediation PR back to `ops-hub`.
