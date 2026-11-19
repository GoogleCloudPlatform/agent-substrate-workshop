# Enterprise AI Agents with Agent Substrate and Gemini platform on GKE

Hands-on curriculum where platform and AI engineers build a stateful incident-response agent inside an isolated sandbox and govern it with managed enterprise state, catalog, security, and observability services.

## Language

### Runtime & Sandboxing

**Incident Actor**:
A stateful, isolated sandbox instance dedicated to investigating and staging remediations for a single production incident, capable of suspending to zero compute between turns.
_Avoid_: Agent pod, worker container, stateless replica

**Actor Boundary**:
The trust and lifecycle perimeter separating a credential-free, snapshot-able Incident Actor from always-on identity, policy, and cloud platform services.
_Avoid_: Pod boundary, cluster perimeter

**Governed Broker**:
The always-on front-door and outbound proxy service outside the Actor Boundary that holds cryptographic agent identity, screens content, enforces tool policy, and attaches credentials on behalf of Incident Actors.
_Avoid_: Sidecar, passthrough proxy, NAT gateway

**Golden Snapshot**:
The shared baseline checkpoint of a warmed template from which every new Incident Actor restores on its first turn.
_Avoid_: Base container image, cold boot state

### State, Catalog & Governance

**Session Ledger**:
The structured, cloud-managed record of an Incident Actor's turn events and state deltas that remains queryable while the Incident Actor is suspended and survives template upgrades.
_Avoid_: Snapshot log, local transcript

**Consolidated Memory**:
Distilled root-cause and operational facts extracted asynchronously from Session Ledgers and recalled across template upgrades and peer Incident Actors.
_Avoid_: Raw chat history, filesystem snapshot

**Procedural Skill**:
A versioned, SHA-256-verified operational runbook bundling instructions and deterministic diagnostic scripts that an Incident Actor pulls on demand at runtime without rebuilding its template.
_Avoid_: Baked-in script, static prompt, container layer

**Remediation Gate**:
A human-in-the-loop approval checkpoint where an Incident Actor pauses and suspends to zero compute before executing a state-changing operational action.
_Avoid_: Synchronous sleep, polling loop

### Incident Environment & Experience

**Target Service**:
The live, intentionally degraded production microservice in the simulation namespace whose failure symptoms, telemetry, and source code the Incident Actor investigates and repairs.
_Avoid_: Mock app, fixture stub

**Operations Hub**:
The self-contained source repository, incident ticket tracker, remediation tool endpoint, and OAuth provider against which Incident Actors clone code, read tickets, and submit human-approved fixes.
_Avoid_: Mock backend, external SaaS stub

**Incident Console**:
The Google Cloud Console-modeled web interface exposed by the Governed Broker, pairing structured operational detail views with a docked conversational triage pane.
_Avoid_: Consumer chat app, marketing dashboard

