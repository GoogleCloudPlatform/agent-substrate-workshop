# Optional lab C — Egress credential injection (outline)

**Goal:** let an actor call an authenticated external API without ever holding the credential.
`EgressRuleEffects.inject_static_headers` lets a matching hostname rule inject a header whose value comes
from an `ate-secret://<provider-class>/<provider-name>/<tail>` reference
(`pkg/proto/ateapipb/ateapi.proto`, `CredentialHeaderInjection`), resolved at request time by dialing a
**credential provider** — a small gRPC service you register with atenet-router
(`cmd/atenet/internal/router/egress/provider.go`).

> **Fact-checked against the substrate checkout at `SUBSTRATE_REF`:** this is real and load-bearing code
> (`pkg/proto/credproviderpb/credprovider.proto`, `cmd/atenet/internal/router/egress/`, unit-tested), but
> there is **no shipped reference provider binary and no docs page** for it yet — nothing under `docs/` or
> `demos/egress` mentions `ate-secret://`. Provider is a plugin API: you write a small gRPC server
> implementing `CredentialProvider.FetchSecret(uri, actor_spiffe_id) -> opaque_bytes`, and configure
> atenet-router to dial it over mTLS (`ProviderDialConfig`: address, CA, client cert, server name). A
> Kubernetes Secret is one reasonable backend for that server to read from, but substrate doesn't provide
> that server for you — this lab is closer to "write a provider" than "flip on a k8s-secret rule."

**You'll need:** a way to run a small gRPC service reachable from `ate-system` (even a `kubectl port-forward`
to a local process for a first pass), and an API key for any HTTPS API to store behind it.

1. Read `pkg/proto/credproviderpb/credprovider.proto` (the one-RPC plugin contract) and
   `cmd/atenet/internal/router/egress/provider.go` and `credentials_test.go` in the substrate checkout — the
   test file is the closest thing to a worked example today.
2. Write a minimal `CredentialProvider` server: `FetchSecret` reads the URI's provider-specific tail (say,
   `ate-secret://demo/api-key`), looks up a Kubernetes Secret in `ate-system`, and returns its bytes. Log
   `actor_spiffe_id` so you can see which actor asked.
3. Wire atenet-router's `ProviderDialConfig` to your server (this is where you'll need to read the flags atenet
   exposes for it — not yet covered by this workshop; expect to trace from `cmd/atenet/internal/router/cmd.go`).
4. Extend `egress-gateway.yaml` with a hostname rule whose `effects.injectStaticHeaders` adds an
   `Authorization` header (`prefix: "Bearer "`) sourced from your `credential_uri`.
5. Add a `/fetch` route to a copy of the agent that calls the API without setting any auth header itself.
   Show that the call succeeds, while `kubectl ate` snapshots, actor logs, and template env all contain no key
   — only the provider ever saw it, and only your gRPC log (not substrate's) records that it was fetched.
6. Compare with the gateway pattern (Module 4: the gateway holds the Vertex credential and the actor never
   calls out at all). When would you use each — an actor egress rule with provider-injected auth, vs. keeping
   the call entirely on the always-on tier?

Status: outline — contributions welcome. A reference `CredentialProvider` implementation (even a
Secret-backed one) and a real `demos/egress`-style example would make this lab far more concrete.
