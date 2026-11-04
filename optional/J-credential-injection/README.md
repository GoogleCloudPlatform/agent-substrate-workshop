# Optional lab J — Egress credential injection (60–90 min)

> **Cluster impact:** Namespace-scoped (`workshop` and `workshop-secrets`), plus a one-line entry in `ate-system`'s `k8s-credential-provider-namespace-policy` `ConfigMap`. Safe to run alongside the rest of the workshop.

**Goal:** let an actor call an authenticated external API without ever holding the credential.
`HttpRuleEffects.replace_headers` lets a matching `https` rule replace a placeholder request header with a value that comes
from an `ate-secret://<provider-class>/<provider-name>/<tail>` reference
(`api/ateapi/v1/ateapi.proto`, `CredentialHeader`), resolved at request time by dialing a
**credential provider**, a small gRPC service (`api/credprovider/v1/credprovider.proto`).

> **Fact-checked against the substrate checkout at `SUBSTRATE_REF` (v1.0.0-rc.1):** substrate ships the built-in
> `k8s.io` provider (`cmd/credential-provider/kubernetes-secrets`) with manifests in
> `manifests/egress-credential-injection/` and a guide in `docs/egress-credential-injection.md`. It resolves
> `ate-secret://k8s.io/default/<namespace>/<secret>/<key>` from a Kubernetes Secret, and it denies by default:
> a ConfigMap (`k8s-credential-provider-namespace-policy`) lists which atespace may read which namespaces.
> In `v0.4.0`+, the egress gateway always runs with MITM TLS termination enabled (`egress-mitm.ate.dev` CA) and
> `ate-setup deploy` requires `--credential-provider` (`{"name":"k8s.io"}`, `{"enabled":false}`, or a custom
> `{"class":…,"endpoint":…}`). The workshop's `make install` passes `--credential-provider='{"name":"k8s.io"}'`,
> which deploys `k8s-credential-provider`, its `NetworkPolicy`, and `atenet-egress` wired to it out of the box.

**You'll need:**
- `ko`, and an API key for any HTTPS API. The steps use `https://httpbin.org`, which needs no real key and
  echoes the headers it received back to you, so you can see the injected header.

1. Read `docs/egress-credential-injection.md`, `api/credprovider/v1/credprovider.proto` (the one-RPC plugin contract), and the `k8s.io`
   provider in `cmd/credential-provider/kubernetes-secrets` and its manifests in `manifests/egress-credential-injection/`.
2. **Verify the credential provider is running.** Because `make install` passes `--credential-provider='{"name":"k8s.io"}'`,
   `k8s-credential-provider` and `atenet-egress` are already deployed and wired together:
   ```bash
   kubectl -n ate-system rollout status deploy/k8s-credential-provider
   kubectl -n ate-system get networkpolicy k8s-credential-provider
   kubectl -n ate-system logs deploy/atenet-egress --all-containers | grep 'egress credential injection enabled'
   make forward-router   # step 5 sends requests through localhost:8080
   ```
   *(If you installed the cluster with `ATE_CREDENTIAL_PROVIDER='{"enabled":false}'`, enable `k8s.io` now with
   `(cd .workshop/substrate && GOWORK=off go run ./cmd/ate-setup deploy atenet --credential-provider='{"name":"k8s.io"}') && make patch-router && make forward-router`.)*
3. Put your API key in a Secret, say `example-api` with key `token` in namespace `workshop-secrets`, and add a
   `k8s-credential-provider-namespace-policy` entry letting atespace `workshop` read `workshop-secrets`. The provider reads its
   policy only at startup, so restart it afterwards:
   ```bash
   kubectl create namespace workshop-secrets
   kubectl -n workshop-secrets create secret generic example-api --from-literal=token=my-demo-token
   kubectl -n ate-system edit configmap k8s-credential-provider-namespace-policy
   # Under data.namespace-policy.yaml, replace `policies: []` with:
   #   policies:
   #   - atespace: workshop
   #     allowedNamespaces: [workshop-secrets]
   kubectl -n ate-system rollout restart deploy/k8s-credential-provider
   kubectl -n ate-system rollout status deploy/k8s-credential-provider
   ```
   (The default `ConfigMap` starts with `policies: []` and a commented-out `team-a` / `ns1` example under `data.namespace-policy.yaml`; replace `policies: []` with the block above.)
4. Extend `egress-gateway.yaml` with an `https` rule whose `effects.replaceHeaders` replaces the
   `Authorization` header (`prefix: "Bearer "`) with the credential from
   `ate-secret://k8s.io/default/workshop-secrets/example-api/token`. `effects` belongs to the `https` rule,
   next to `hostnames`; [`egress-fetch.yaml`](egress-fetch.yaml) has the finished shape.
5. Add a `/fetch` route to a copy of the agent that calls the API with a placeholder `Authorization: Bearer placeholder` header
   ([`app/cmd/agent-fetch`](../../app/cmd/agent-fetch/main.go) is a finished version). The actor also needs to
   trust both the egress gateway's MITM CA (`egress-mitm.ate.dev`) and public root CAs (`system-roots.ate.dev`).
   [`template.yaml.tmpl`](template.yaml.tmpl) is agent-v3 plus a `systemInfo` volume with
   `trustBundle.names: [egress-mitm.ate.dev, system-roots.ate.dev]` and `SSL_CERT_FILE`
   (substrate's `docs/egress-trust-bundle.md` explains both):
   ```bash
   export FETCH_IMAGE="$(cd app && ko build --base-import-paths ./cmd/agent-fetch)"
   scripts/render.sh optional/J-credential-injection/template.yaml.tmpl | kubectl ate create actor-template -f -
   kubectl ate get actor-template agent-fetch-v1 -a workshop                 # repeat until GOLDEN TAG is set (~20s)
   kubectl ate create actor fetch1 --template=agent-fetch-v1 -a workshop
   scripts/egress.sh apply optional/J-credential-injection/egress-fetch.yaml --actor=fetch1
   curl -s -H 'ate-target-actor: workshop/fetch1' 'localhost:8080/fetch?url=https://httpbin.org/headers'
   curl -s -H 'ate-target-actor: workshop/fetch1' 'localhost:8080/fetch?url=https://example.com/'   # egress denied
   ```
   httpbin's reply shows `"Authorization": "Bearer my-demo-token"`, replacing the agent's placeholder. Now show
   the key is nowhere the actor's owner can see: `kubectl ate get actor-template agent-fetch-v1 -a workshop -o yaml`,
   `kubectl ate get actor fetch1 -a workshop -o yaml`, `scripts/egress.sh get --actor=fetch1`, and the actor's
   logs in Logs Explorer all hold only the `ate-secret://` URI. Only the provider and the egress gateway ever saw
   the key. (One caveat: httpbin *echoes* the header back, so after this call the key is in the actor's memory,
   and in its next `SNAPSHOT_FIDELITY_MEMORY` snapshot. A real API doesn't echo credentials.)
6. Compare with the gateway pattern (Module 4: the gateway holds the model credential and the actor never
   calls out at all). When would you use each — an actor egress rule with provider-injected auth, vs. keeping
   the call entirely on the always-on tier?

## Clean up

```bash
kubectl ate delete actor fetch1 -a workshop
kubectl ate delete actor-template agent-fetch-v1 -a workshop
kubectl delete namespace workshop-secrets --ignore-not-found
```
