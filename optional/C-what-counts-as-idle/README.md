# Optional lab C — What counts as idle? (20 min)

> **Cluster impact:** Namespace-scoped. Changes your agent and adds template `agent-v4`; the gateway and other actors are untouched.

**Goal:** see what idle-suspend does to work that runs with no request in flight, and fix it by having the
actor tell the gateway it's busy.

**You'll need:** the Module 5 end state: the gateway with `IDLE_AFTER=3s`, the swarm, and its egress policy
(`make catch-up MODULE=6` gets you there). Keep `make forward-gateway` running (localhost:8081).

Module 5 step 7 froze a request that skipped the gateway. This lab goes through the gateway, but the work
outlives the request.

## 1. A background job gets frozen

Every actor has `POST /jobs?steps=N`. It answers `202` right away and does the work in the background, about
0.3s per step. Give swarm-13 about 18s of work through the gateway and watch it:
```bash
curl -s -X POST 'localhost:8081/actors/swarm-13/jobs?steps=60'   # {"heartbeat":false,"steps":60}
while true; do kubectl ate get actor swarm-13 -a workshop; sleep 1; done   # Ctrl-C once it's SUSPENDED
```
swarm-13 is SUSPENDED about 3s later. The gateway saw a request that finished in half a second and then
nothing, so by its rules the actor was idle. The job was frozen mid-loop.

Wait half a minute, then wake swarm-13 and read its log in one go. Logs only come from a running actor, and
this one is back asleep 3s later:
```bash
curl -s -o /dev/null localhost:8081/actors/swarm-13/state; kubectl ate logs actor swarm-13 -a workshop | grep 'job step' | grep -v 'gap 0\.'
```
Each line is JSON. The log can include lines from earlier wakes on the same worker, so this keeps only the
lines with a big gap, and the last one is where the job was frozen:
`{"time":"…","message":"… job step 14/60 (gap 55.8s)"}`. The job picked up where it froze. Wake it
again a few seconds later and it has moved on about a dozen steps. The job only runs in the 3–4s after each
time someone talks to swarm-13.

## 2. Tell the gateway you're busy

The gateway only knows about requests. To know about the job, it has to hear from the actor while the job
runs. But the actor can't just call "I'm busy", because it doesn't know its own name. Every actor's hostname is
`actor`, every clone starts from the same memory, and the router strips the routing header before the request
reaches the actor. So the gateway sends a callback URL with each job, in a `Busy-URL` header:
`http://gateway.workshop.svc.cluster.local/actors/swarm-13/busy`. A POST there restarts that actor's idle clock.
The job in step 1 got one too; agent-v3 just ignores it.

In `app/cmd/agent/main.go`, find `WORKSHOP(lab-c)` and set `Heartbeat: true`. The job then POSTs its
`Busy-URL` every second until it finishes (`app/internal/agent/work.go`). Build it into a new template. A
golden snapshot can't change, so this is agent-v4:
```bash
make build-agent
scripts/render.sh optional/C-what-counts-as-idle/template.yaml.tmpl | kubectl ate create actor-template -f -
kubectl ate get actor-template agent-v4 -a workshop     # repeat until GOLDEN TAG is set (~25s)
kubectl ate create actor cruncher --template=agent-v4 -a workshop
scripts/egress.sh apply modules/04-make-it-an-agent/manifests/egress-gateway.yaml --actor=cruncher
```
The egress policy matters. The heartbeat is an outbound call to the gateway, and a new actor has no policy, so
without one every heartbeat gets a connection reset. Give the policy 10s to reach the egress cache
(Module 4 step 4).

## 3. Run the job again

```bash
curl -s -X POST 'localhost:8081/actors/cruncher/jobs?steps=60'   # {"heartbeat":true,"steps":60}
while true; do kubectl ate get actor cruncher -a workshop; sleep 1; done   # Ctrl-C once it's SUSPENDED
```
This time cruncher stays RUNNING for the whole job and is suspended about 3s after it ends. Count the
heartbeats on the gateway:
```bash
curl -s localhost:8081/metrics | grep '^workshop_busy_heartbeats_total'   # one per second of work, ~17
```
The counter lives in the gateway pod's memory, so it starts from 0 whenever the gateway rolls
(`make idle-off`/`idle-on` in labs D and F, or any `kubectl set env`). Compare before/after within one run.
cruncher was suspended again before you could read its log, so check the job in Cloud Logging. Give it ~15s
after the job ends for the log to arrive:
```bash
PROJECT_ID="$(sed -n 's/^PROJECT_ID=//p' workshop.env)"   # your project, not whatever gcloud defaults to
gcloud logging read 'resource.type="k8s_container" AND labels."ate.actor.name"="cruncher" AND jsonPayload.textPayload:"job done"' \
  --project "$PROJECT_ID" --freshness=10m --format='value(jsonPayload.textPayload)'   # …job done: 60 steps in 17.5s, max gap 0.3s
```
No gap: the job never froze. The gateway log shows the suspend a few seconds after the job ended:
`kubectl -n workshop logs deploy/gateway | grep cruncher`.

Now send the same job straight to the router (`make forward-router`, localhost:8080):
```bash
curl -s -X POST -H 'ate-target-actor: workshop/cruncher' 'localhost:8080/jobs?steps=60'   # {"heartbeat":false,...}
```
No `Busy-URL`, so nothing to call: cruncher is frozen mid-job, just like swarm-13.

Run the gateway job once more. The frozen job wakes up too, and both finish: Cloud Logging shows one
`job done` for each, and the old one reports a gap as long as it was frozen (about a minute). The two share the CPU, so each takes about
twice as long and you'll count about twice the heartbeats.

## What just happened
- **Idle means "nothing I know of is running."** The gateway knows about requests, and now about heartbeats.
  Anything else in the sandbox gets frozen along with it: a background job, a timer, a queue consumer.
- **Awake is all or nothing.** A heartbeat keeps the whole sandbox running, not just the job that sends it.
  Anything else that was frozen gets to run too.
- **The deciding party hands out the callback.** The actor can't name itself, so the gateway, which knows
  whom it's talking to, puts the name in the URL.
- **Heartbeats trade density for correctness.** A heartbeat every second is safe against a 3s `IDLE_AFTER`
  checked every second. Beat less often than that and the job freezes between beats.

## Think about it
- A job that hangs keeps beating, so its actor never sleeps. Cap how long a job may keep an actor awake, or
  beat only while the job makes progress.
- `/busy` has no authentication. Anything that can reach the gateway can keep any actor awake. A real system
  would sign the `Busy-URL` per job, or accept heartbeats only from the actor it names.
- Could the job say "done" instead of the gateway waiting 3s? What would you do with that?

## Clean up
```bash
kubectl ate delete actor cruncher -a workshop --any-state
scripts/egress.sh delete --actor=cruncher
kubectl ate delete actor-template agent-v4 -a workshop
```
Keep `Heartbeat: true` or set it back: agent-v3's golden snapshot doesn't change either way.

## Next

Back to [Module 7 — Wrap-up](../../modules/07-wrap-up/README.md) for the rest of the optional labs.
