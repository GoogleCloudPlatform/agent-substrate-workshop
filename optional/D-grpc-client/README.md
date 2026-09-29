# Optional lab D — Your own gRPC client (outline)

> **Cluster impact:** Namespace-scoped. A client program that lists, creates, suspends and resumes your own actors.

**Goal:** write a Go program that drives ateapi directly: list, create, suspend, and resume actors, without
going through `kubectl ate` or the gateway.

**You'll need:** the workshop's `app/internal/ateclient` package (`Dial`, `TLSConfig`, the file-token
credential — confirmed present in this checkout) and `scripts/ate-api.sh` (which already wraps
`scripts/lib.sh`'s `port_forward`, so sourcing it never hand-rolls a `kubectl port-forward`).

1. `source scripts/ate-api.sh` to get `ATE_API_ADDR`, `ATE_TOKEN_FILE`, and `ATE_CA_FILE` — the same
   address/token/CA `kubectl-ate` itself uses (`api.ate-system.svc:443` via port-forward, a short-lived
   token audienced to `api.ate-system.svc`, and the `servicedns.podcert.ate.dev/identity`
   ClusterTrustBundle).
2. Copy `app/cmd/egressctl/main.go` to `app/cmd/fleetctl/main.go` and keep its flag parsing and
   `ateclient.Dial` call — `Dial` returns an `ateapipb.ControlClient`, which already has every RPC you need
   (`ListActors`, `ResumeActor`, `SuspendActor`, …).
3. Implement `list` (`ListActors` with paging — see `getActorsCmd` in substrate's
   `cmd/kubectl-ate/internal/cmd/actor.go` for the paging loop shape), `wake NAME` (`ResumeActor`), and
   `sleep-all` (`SuspendActor` on every actor whose `state` is `ACTOR_STATE_RUNNING`).
4. Add a fake-server test with `app/internal/testutil.FakeControl`, the way `internal/idle`'s tests do
   (`StartFake` spins up an in-process fake ateapi over TLS). Note: at this checkout `FakeControl` implements
   `ListActors` and `SuspendActor` (plus the EgressPolicy verbs) but not `ResumeActor` — you'll need to add a
   small `ResumeActor` stub to it before `wake`'s test can run against the fake.
5. Stretch: `watch`, which polls `ListActors` on an interval and prints state transitions (diff the actor
   list between polls).

Status: outline — contributions welcome.
