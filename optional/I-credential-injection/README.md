# Optional lab I — Egress credential injection (60–90 min)

> **Cluster impact:** Changes the cluster: **reinstalls substrate** with the experimental egress dataplane. Do it last. The workshop keeps working afterwards, provided you re-apply the router patch (step 2); on a shared or long-lived cluster, use a fresh one instead.

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
> none of these; step 2 adds them. Run end to end on a GKE cluster on 2026-09-28.

**You'll need:**
- **Everything else done first.** This is the last lab because step 2 reinstalls substrate. Finish or clean up
  labs A–H before you start: lab F's and lab G's clean-ups in particular, since the reinstall restarts the router
  and the api-server underneath them.
- **Or a fresh cluster.** If you'd rather not touch your workshop cluster, create a new one with the runbook's
  cluster command and run `make install` and `make catch-up MODULE=5` there first; the steps below are the same.
- `ko`, and an API key for any HTTPS API. The steps use `https://httpbin.org`, which needs no real key and
  echoes the headers it received back to you, so you can see the injected header.

1. Read `pkg/proto/credproviderpb/credprovider.proto` (the one-RPC plugin contract), then the reference
   provider in `cmd/credential-provider/kubernetes-secrets` and its three manifests.
2. **Reinstall substrate** from the checkout with the three flags above, then **re-apply the workshop's router
   settings**. `ate-setup deploy` re-renders `atenet-router` with substrate's defaults (a 5s parked-request
   budget and a 60s grace period); `make install` can't restore them for you, because it would redeploy
   *without* the experimental flags. Then build and apply the reference provider. Its Deployment's image is a
   `ko://` reference, so `kubectl apply` it only after `ko resolve`:
   ```bash
   source scripts/lib.sh && load_env
   (cd .workshop/substrate && GOWORK=off KO_DOCKER_REPO="$KO_DOCKER_REPO" go run ./cmd/ate-setup deploy ate-system \
       --experimental-use-sdsmint --atenet-dataplane=envoy --experimental-egress-credential-injection)
   make patch-router              # --parked-request-budget=60s and a 120s grace, as after `make install`
   (cd .workshop/substrate && export GOWORK=off KO_DOCKER_REPO &&
     ldflags=(); while IFS= read -r l; do [[ -n "$l" ]] && ldflags+=("--ldflags=$l"); done < <(make -s ldflags) &&
     ./hack/run-tool.sh ko resolve --base-import-paths "${ldflags[@]}" -f manifests/egress-credential-injection/) |
     kubectl apply -f -
   kubectl -n ate-system logs deploy/atenet-egress --all-containers | grep 'egress credential injection enabled'
   ```
   The reinstall restarts the router, api-server and egress gateway, so your port-forwards die. Restart the
   ones you use (`scripts/lib.sh`'s `stop_port_forward router` then `make forward-router`, and
   `make forward-gateway`).
3. Put your API key in a Secret, say `example-api` with key `token` in namespace `workshop-secrets`, and add a
   `namespace-policy.yaml` entry letting atespace `workshop` read `workshop-secrets`. The provider reads its
   policy only at startup, so restart it afterwards:
   ```bash
   kubectl create namespace workshop-secrets
   kubectl -n workshop-secrets create secret generic example-api --from-literal=token=my-demo-token
   kubectl -n ate-system edit configmap k8s-credential-provider-namespace-policy
   #   policies:
   #   - atespace: workshop
   #     allowedNamespaces: [workshop-secrets]
   kubectl -n ate-system rollout restart deploy/k8s-credential-provider
   ```
   (The manifests also create a sample `ns1` namespace and Secret for atespace `team-a`. You don't need them.)
4. Extend `egress-gateway.yaml` with a hostname rule whose `effects.injectStaticHeaders` adds an
   `Authorization` header (`prefix: "Bearer "`) sourced from
   `ate-secret://k8s.io/default/workshop-secrets/example-api/token`. `effects` belongs to the *hostname rule*,
   next to `patterns`; [`egress-fetch.yaml`](egress-fetch.yaml) has the finished shape.
5. Add a `/fetch` route to a copy of the agent that calls the API without setting any auth header itself
   ([`app/cmd/agent-fetch`](../../app/cmd/agent-fetch/main.go) is a finished version). The actor also needs to
   trust the egress gateway's CA: under sdsmint the gateway terminates every HTTPS connection with a certificate
   it minted. [`template.yaml.tmpl`](template.yaml.tmpl) is agent-v3 plus a `systemInfo` trust-bundle volume and
   `SSL_CERT_FILE`/`SSL_CERT_DIR` (substrate's `docs/egress-trust-bundle.md` explains both):
   ```bash
   export FETCH_IMAGE="$(cd app && ko build --base-import-paths ./cmd/agent-fetch)"
   scripts/render.sh optional/I-credential-injection/template.yaml.tmpl | kubectl ate create actor-template -f -
   kubectl ate create actor fetch1 --template=agent-fetch-v1 -a workshop     # once the golden tag appears
   scripts/egress.sh apply optional/I-credential-injection/egress-fetch.yaml --actor=fetch1
   curl -s -H 'ate-target-actor: workshop/fetch1' 'localhost:8080/fetch?url=https://httpbin.org/headers'
   curl -s -H 'ate-target-actor: workshop/fetch1' 'localhost:8080/fetch?url=https://example.com/'   # egress denied
   ```
   httpbin's reply shows `"Authorization": "Bearer my-demo-token"`, although the agent never set it. Now show
   the key is nowhere the actor's owner can see: `kubectl ate get actor-template agent-fetch-v1 -a workshop -o yaml`,
   `kubectl ate get actor fetch1 -a workshop -o yaml`, `scripts/egress.sh get --actor=fetch1`, and the actor's
   logs in Logs Explorer all hold only the `ate-secret://` URI. Only the provider and the egress gateway ever saw
   the key. (One caveat: httpbin *echoes* the header back, so after this call the key is in the actor's memory,
   and in its next FULL snapshot. A real API doesn't echo credentials.)
6. Compare with the gateway pattern (Module 4: the gateway holds the Vertex credential and the actor never
   calls out at all). When would you use each — an actor egress rule with provider-injected auth, vs. keeping
   the call entirely on the always-on tier?
7. Back to [lab A's step 5](../A-env/README.md#5-lock-down-egress-env-sets-no-policy--after-lab-i): HTTPS
   hostname rules now work, so the PyPI lock-down can run.

After the reinstall, the rest of the workshop keeps working (Modules 2–5 `make verify` pass, `buddy` still
chats through the gateway), provided you ran `make patch-router`.
