# Module 5 — Density and idle (10 min)

**Goal:** run 20 agents on 2 workers. See what happens when everyone wants a worker at once, and add
the one thing Substrate deliberately doesn't do for you: **decide when an actor is idle**.

## Steps

1. **Clear the decks.** Suspend `buddy`, which is still RUNNING and holding a worker.
   ```bash
   kubectl ate suspend actor buddy -a workshop
   kubectl ate get actors -a workshop
   ```
2. **Create a swarm.** Creating actors is cheap: each one is a record pointing at `agent-v3`'s golden snapshot.
   ```bash
   for i in $(seq -w 1 20); do kubectl ate create actor swarm-$i --template=agent-v3 -a workshop; done
   make egress POLICY=modules/04-make-it-an-agent/manifests/egress-gateway.yaml ALL=1
   kubectl ate get actors -a workshop
   ```
   `ALL=1` applies the policy to every actor that exists **now**. Actors created later need it applied again.
   That includes `hello` and `memo`, which never call a model. It's harmless here, but it isn't least
   privilege; in production, apply policies per template or per actor, as Module 4 did for `buddy`.
3. **Fill both workers.** Restart the gateway port-forward if it died (`make forward-gateway`).
   ```bash
   curl -s localhost:8081/chat/swarm-01 -d '{"message":"hi"}' | jq -c '{turn, backend}'
   curl -s localhost:8081/chat/swarm-02 -d '{"message":"hi"}' | jq -c '{turn, backend}'
   kubectl ate get workers -n workshop           # both busy
   ```
4. **Ask a third actor.** *(Cut line: the instructor may just describe this step.)*
   ```bash
   time curl -s localhost:8081/chat/swarm-03 -d '{"message":"hi"}'; echo
   ```
   The router **parks** the request while it looks for a free worker. After the 60s parked-request budget it gives up
   with a `503`. swarm-01 and swarm-02 are done talking, but they're still RUNNING:
   **Substrate never suspends an actor on its own.** It can't know whether your agent is idle, thinking, or
   waiting on a tool.
5. **Teach the gateway to suspend idle actors.** The gateway sees every chat turn, so it knows when each actor
   last did something. With `IDLE_AFTER` set, it suspends RUNNING actors that have had no request in flight for that long.
   ```bash
   kubectl -n workshop set env deploy/gateway IDLE_AFTER=3s
   kubectl -n workshop rollout status deploy/gateway   # wait for this to finish before the next line
   make forward-gateway                                # restarts the forward onto the new gateway pod
   while true; do clear; kubectl ate get actors -a workshop; sleep 1; done   # swarm-01/02 fall asleep within ~5s; Ctrl-C
   ```
   The code is in `app/internal/idle/idle.go`: a tracker fed by the front door, and a loop (about 200 lines
   total) that checks every second (`IDLE_CHECK_EVERY`) and calls `SuspendActor` over gRPC with the gateway's
   own identity.
6. **Oversubscribe on purpose.** Four actors, two workers, all at once:
   ```bash
   for a in swarm-03 swarm-04 swarm-05 swarm-06; do
     (curl -s -o /dev/null -w "$a %{http_code} %{time_total}s\n" localhost:8081/chat/$a -d '{"message":"hi"}') &
   done; wait
   ```
   All four return `200`. Two answered right away. The other two parked until the idle-suspender freed their workers,
   so they took about 5–8s: 3s of idle, up to 1s until the next check, a suspend and a restore, plus the
   1–2s model call itself.
7. **What the gateway can't see** (skippable). Every actor has a `/work?steps=N` endpoint that burns CPU for about
   0.3s per step inside the request. Ask swarm-03 for about 18s of work, but go straight to the router instead of
   through the gateway:
   ```bash
   make forward-router    # if the Module 2 forward died
   curl -s -w '\nHTTP %{http_code} in %{time_total}s\n' -H 'ate-target-actor: workshop/swarm-03' 'localhost:8080/work?steps=60'
   ```
   It fails with a `502` after 3–5s. The gateway never saw this request, so as far as its tracker knew, swarm-03
   was idle, and the gateway suspended it mid-job. Substrate froze the whole sandbox, loop and all. The work isn't
   lost: the next time anything wakes swarm-03, the loop picks up at the step where it froze and finishes, for a
   caller who has already gone. Send the same request through the gateway
   (`curl -s 'localhost:8081/actors/swarm-03/work?steps=60'`) and it returns `200`: the in-flight request keeps
   the idle clock stopped. It takes about 30s rather than 18s, because waking swarm-03 also restarts the frozen job,
   and the two share one CPU. To see both finish, wake swarm-03 and read its logs in one go (logs only come from
   a running actor, and it's back asleep 3s later):
   ```bash
   curl -s -o /dev/null localhost:8081/actors/swarm-03/state; kubectl ate logs actor swarm-03 -a workshop | grep 'work done'
   ```
   Work that outlives its request (a background job, a queue consumer) needs the actor to tell whoever decides
   "idle" that it's busy. The tracker has a `Touch` method for that.

> **Callout: clones share a past.** Every swarm actor was restored from the same golden snapshot, so
> anything computed *before* the snapshot is identical in all of them:
> ```bash
> for a in swarm-03 swarm-04; do curl -s localhost:8081/actors/$a/state | jq -r .bootID; done
> ```
> Same `bootID`. If your agent generates IDs, seeds randomness, or fetches credentials at startup, do it on
> the first request (or after restore), not at boot.
>
> Be careful with time, too. The sandbox clock jumps forward on restore. A duration measured across a suspend
> includes the nap, and a timing loop run just after a restore can come out wrong while the clock catches up.
> Count work, not wall time, if a suspend can land in the middle.

## What just happened
- **Density** comes from suspending. 20 actors fit on 2 workers because at most 2 are awake at a time. Why 2: each
  worker's `cpu: 1` limit is a budget its actors share, and each actor asks for `cpu: 1`. Substrate workers can
  host several actors at once, so a bigger worker limit (or smaller actors) packs more awake actors per worker.
- **Parking** smooths bursts: a request waits, up to the router's budget, for the actor to get a worker.
- **Idle detection is your job**, because only your app knows what "idle" means. A front door that sees all traffic
  is a natural place for it. Real systems also scale the WorkerPool itself (optional lab F).
- **Suspend early.** Measured on this cluster: a snapshot is about 1 MB (P95 about 4 MB). Suspend takes about 0.4s
  and restore about 0.25s. The five GCS writes per snapshot cost about $0.000025, while a c3-standard-4 worker
  costs about $0.00006 a second. So an actor idle for more than a second or two is cheaper asleep. That's why
  `IDLE_AFTER` is only 3s here. The real limit is how often you're willing to make the next request wait for a
  restore.
- **Idle means "nothing I know of is running."** A suspend freezes everything in the sandbox, including work the
  gateway never saw (step 7).

## Checkpoint
```bash
make verify MODULE=5
```
**Stuck?** Run `make catch-up MODULE=6`.

Next: [Module 6 — Observe the fleet](../06-observe/README.md)
