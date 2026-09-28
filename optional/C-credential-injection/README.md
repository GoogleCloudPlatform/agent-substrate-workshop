# Optional lab C — Egress credential injection (outline)

**Goal:** let an actor call an authenticated external API without ever holding the credential.
`EgressRuleEffects.inject_static_headers` lets a matching hostname rule inject a header whose value comes
from an `ate-secret://<provider-class>/<provider-name>/<tail>` reference
(`pkg/proto/ateapipb/ateapi.proto`, `CredentialHeaderInjection`), resolved at request time by dialing a
**credential provider**, a small gRPC service (`pkg/proto/credproviderpb/credprovider.proto`).

> **Fact-checked against the substrate checkout at `SUBSTRATE_REF` (v0.2.0):** substrate now ships a
> reference provider, `cmd/credential-provider/kubernetes-secrets`, with manifests in
> `manifests/egress-credential-injection/`. It resolves
> `ate-secret://k8s.io/default/<namespace>/<secret>/<key>` from a Kubernetes Secret, and it denies by default:
> a ConfigMap (`namespace-policy.yaml`) lists which atespace may read which namespaces. The injection runs on
> the **egress gateway's** decrypted MITM leg, not on atenet-router, so it's **experimental and off by default**.
> `ate-setup deploy` needs `--experimental-egress-credential-injection`, which in turn requires
> `--experimental-use-sdsmint` and `--atenet-dataplane=envoy` (`cmd/ate-setup/internal/cmd/root.go`;
> `hack/experimental-egress-credential-injection.sh` explains the wiring). The workshop's `make install` sets
> none of these, and this lab hasn't been run end to end.

**You'll need:** a cluster you can reinstall substrate on with the experimental flags above, and an API key
for any HTTPS API.

1. Read `pkg/proto/credproviderpb/credprovider.proto` (the one-RPC plugin contract), then the reference
   provider in `cmd/credential-provider/kubernetes-secrets` and its three manifests.
2. Reinstall substrate from the checkout with the three flags above (`.workshop/substrate`,
   `go run ./cmd/ate-setup deploy ate-system --experimental-use-sdsmint --atenet-dataplane=envoy
   --experimental-egress-credential-injection`), then apply `manifests/egress-credential-injection/`.
3. Put your API key in a Secret, say `example-api` with key `token` in namespace `workshop-secrets`, and add
   a `namespace-policy.yaml` entry letting atespace `workshop` read `workshop-secrets`.
4. Extend `egress-gateway.yaml` with a hostname rule whose `effects.injectStaticHeaders` adds an
   `Authorization` header (`prefix: "Bearer "`) sourced from
   `ate-secret://k8s.io/default/workshop-secrets/example-api/token`.
5. Add a `/fetch` route to a copy of the agent that calls the API without setting any auth header itself.
   Show that the call succeeds, while `kubectl ate` snapshots, actor logs, and template env all contain no key.
   Only the provider and the egress gateway ever saw it.
6. Compare with the gateway pattern (Module 4: the gateway holds the Vertex credential and the actor never
   calls out at all). When would you use each — an actor egress rule with provider-injected auth, vs. keeping
   the call entirely on the always-on tier?

Status: outline — contributions welcome. A run of it on a real cluster, with the exact commands and output,
would make this lab concrete.
