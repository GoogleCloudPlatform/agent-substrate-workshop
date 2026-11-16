# 3. Defense-in-Depth Governance Across Ingress `GCPAgentGateway` and Outbound `sre-gateway`

Status: accepted

Module 05 enforces governance across two complementary layers: declarative GKE **Agent Gateway** resources (`Gateway`, `GCPAgentGateway`, and `GCPAuthzPolicy` in `networking.gke.io/v1`) on the `CLIENT_TO_AGENT` ingress perimeter, paired with inline **Model Armor** (`SanitizeUserPrompt` / `SanitizeModelResponse`), **Agent Registry** MCP `readOnlyHint` / `destructiveHint` policy evaluation, and **Agent Identity Auth Manager** (`adk/v2/auth/gcp`) 3LO token injection inside `sre-gateway`'s outbound broker pipeline.

## Context & Rationale

1. **Deterministic classroom behavior across GKE `1.37+` regions**: Standard L7 `Gateway/sre-ingress-gateway` (`gke-l7-regional-external-managed`) and `GCPAuthzPolicy/sre-ingress-authz-policy` (`<= 10` paths, `LoadBalancingScheme: EXTERNAL_MANAGED`) reconcile immediately for external HTTP `:80` traffic, while `GCPAgentGateway/sre-client-to-agent-gateway` attaches to internal `Gateway/sre-agent-gateway` (`gke-l7-rilb`) so `thetis` does not switch `sre-ingress-gateway`'s `AuthzPolicy` off `EXTERNAL_MANAGED`. Hosted controller translation of `policyProfile: CONTENT_AUTHZ` and `operations[].mcp` can vary by regional rollout flag, and full `AGENT_TO_ANYWHERE` egress requires GKE Ambient Networking + Secure Web Proxy (~15–20m cold setup).
2. **Real cloud APIs, fast setup**: Calling the live **Model Armor** API against a provisioned `ModelArmorTemplate` and evaluating live **Agent Registry** tool annotations inside `sre-gateway` guarantees deterministic HTTP `403` / prompt-injection blocks (`INC-409` and unapproved destructive MCP tools) in `< 10` minutes of lab setup, while keeping Ambient Mesh + Secure Web Proxy as an optional overlay in `07-wrap-up`.
