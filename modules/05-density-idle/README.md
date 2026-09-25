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
   kubectl -n workshop set env deploy/gateway IDLE_AFTER=20s
   kubectl -n workshop rollout status deploy/gateway   # wait for this to finish before the next line
   make forward-gateway                                # restarts the forward onto the new gateway pod
   while true; do clear; kubectl ate get actors -a workshop; sleep 2; done   # swarm-01/02 fall asleep ~20–25s later; Ctrl-C
   ```
   The code is in `app/internal/idle/idle.go`: a tracker fed by the front door, and a loop (about 200 lines
   total) that calls `SuspendActor` over gRPC with the gateway's own identity.
6. **Oversubscribe on purpose.** Four actors, two workers, all at once:
   ```bash
   for a in swarm-03 swarm-04 swarm-05 swarm-06; do
     (curl -s -o /dev/null -w "$a %{http_code} %{time_total}s\n" localhost:8081/chat/$a -d '{"message":"hi"}') &
   done; wait
   ```
   All four return `200`. Two answered right away. The other two parked until the idle-suspender freed their workers,
   so they took about 20–30s longer.

> **Callout: clones share a past.** Every swarm actor was restored from the same golden snapshot, so
> anything computed *before* the snapshot is identical in all of them:
> ```bash
> for a in swarm-03 swarm-04; do curl -s localhost:8081/actors/$a/state | jq -r .bootID; done
> ```
> Same `bootID`. If your agent generates IDs, seeds randomness, or fetches credentials at startup, do it on
> the first request (or after restore), not at boot.

## What just happened
- **Density** comes from suspending. 20 actors fit on 2 workers because at most 2 are awake at a time.
- **Parking** smooths bursts: a request waits, up to the router's budget, for the actor to get a worker.
- **Idle detection is your job**, because only your app knows what "idle" means. A front door that sees all traffic
  is a natural place for it. Real systems also scale the WorkerPool itself (optional lab G).

## Checkpoint
```bash
make verify MODULE=5
```
**Stuck?** Run `make catch-up MODULE=6`.

Next: [Module 6 — Observe the fleet](../06-observe/README.md)
