# Optional lab D — Tags, clone, revert (20 min)

> **Cluster impact:** Namespace-scoped. Works on `buddy` and a clone. It switches the gateway's idle-suspend off (`make idle-off`) and back on (`make idle-on`) at the end.

Starts from the end of Module 4 or later (`make catch-up MODULE=5` gets you a `buddy` actor on `agent-v3`
if you skipped ahead).

A **tag** takes its own copy of an actor's external snapshot. Suspending the actor again — or deleting it —
can't touch what the tag copied. You can create a new actor from a tag (**clone**) or send a diverged actor
back to its own last snapshot (**revert**).

> **Fact-checked against the substrate source** (`cmd/kubectl-ate/internal/cmd/tag.go`,
> `revert_actor.go`, `actor.go` at `SUBSTRATE_REF`): `create tag` takes `--actor` and `-a/--atespace`, not a
> snapshot fidelity — `--scope` here means the *tag's* visibility (`atespace` or `published`), not the
> MEMORY/VOLUMES snapshot fidelity you set on a template. And `revert actor` takes **no `--tag` flag at
> all**: it sends a crashed or running actor (or a paused actor with a node-local snapshot) back to **its own last external snapshot**, discarding
> only the execution since then. See "What to notice" below for why that's actually the more useful demo.

**First, turn off idle-suspend for this lab.** Module 5's gateway suspends any actor that's been idle
for 3s, and a suspend *is* a new snapshot: it would overwrite buddy's "pineapple" snapshot with whatever
buddy said last, and leave it SUSPENDED, a state `revert` refuses. `make idle-off` sets `IDLE_AFTER=0s` and
restarts the gateway port-forward on localhost:8081 (the gateway pod rolls); `make idle-on` at the end puts
it back.

```bash
make idle-off             # also (re)starts the gateway port-forward; never hand-roll `kubectl port-forward &`
curl -s localhost:8081/chat/buddy -d '{"message":"The secret word is pineapple."}' | jq .reply

# Suspend before tagging. An actor keeps exactly one external snapshot, replaced by its next suspend;
# only a tag survives that. (docs/api-guide.md: "To keep the snapshot a suspend just wrote, tag it with
# CreateTag while the actor is still suspended.")
kubectl ate suspend actor buddy -a workshop
kubectl ate create tag knows-pineapple --actor buddy -a workshop   # --scope defaults to "atespace"
kubectl ate get tags -a workshop

# Clone: a new actor that borrows buddy's tagged snapshot instead of agent-v3's golden snapshot.
kubectl ate create actor buddy-2 --template=agent-v3 --tag knows-pineapple -a workshop
make egress POLICY=modules/04-make-it-an-agent/manifests/egress-gateway.yaml ACTOR=buddy-2
curl -s localhost:8081/chat/buddy-2 -d '{"message":"What is the secret word?"}' | jq .reply   # pineapple

# Same past, same bootID: buddy-2 hasn't suspended yet (idle-suspend is off), so it's running the exact FULL
# snapshot buddy had when you tagged it (module 3's "restore process, same bootID" callout, module 5's
# "clones share a past" callout — this is the same mechanism, just via a tag instead of a golden snapshot).
curl -s localhost:8081/actors/buddy/state   | jq -r .bootID
curl -s localhost:8081/actors/buddy-2/state | jq -r .bootID    # same

# Diverge buddy, then revert it — not to the tag, to its OWN last snapshot (still "pineapple",
# since it hasn't been suspended again).
curl -s localhost:8081/chat/buddy -d '{"message":"Forget that. The secret word is now mango."}' | jq .reply
kubectl ate revert actor buddy -a workshop
curl -s localhost:8081/chat/buddy -d '{"message":"What is the secret word?"}' | jq .reply     # pineapple again
curl -s localhost:8081/actors/buddy/state | jq '{turns, bootID}'                              # turns rolled back; bootID matches buddy-2's

make idle-on              # restore Module 5's IDLE_AFTER=3s
```
With idle-suspend on, the diverge-then-revert above only works if both commands run within 3s: the
suspender would snapshot "mango" first. That's the lesson of this lab from the other side: anything that
suspends replaces the actor's one external snapshot, and only a tag keeps an older one.

## What to notice
- **Tag scope vs. snapshot fidelity are different knobs.** `--scope atespace|published` on a tag
  controls who can read it (its own atespace, or any atespace via a published reference) — it does not
  choose MEMORY vs. VOLUMES. Snapshot fidelity is set once, on the template, via `snapshotConfig.preferredFidelity`
  (`agent-v3` uses `SNAPSHOT_FIDELITY_MEMORY`, from Module 4).
- **A clone shares its past with the source** (same `bootID`, same conversation), exactly like the
  golden-snapshot callout in Module 5 — except here the shared point is whatever moment you tagged, not the
  template's golden snapshot. Anything that must be unique per actor (generated IDs, seeded randomness,
  fetched credentials) has to be regenerated on the first request after the clone starts, not at boot.
- **Revert is not "go to this tag."** It has no target snapshot argument — it discards a crashed or running
  actor's current execution (or a paused actor's node-local snapshot) and drops it back to SUSPENDED against whatever external snapshot it
  already has. That's still a real "undo," just scoped to "since the last suspend," not to an arbitrary
  point in history. To revert to an arbitrary earlier point, delete the actor and clone a fresh one from a
  tag instead — which is exactly what the clone step above did.
- Revert rolls back **everything in the snapshot's fidelity**, including `/data` for a `VOLUMES`-fidelity template.
- Use cases: checkpoints before risky tool calls, forking a conversation (clone), an "undo" for the last
  turn or tool call (revert), and warm-starting many actors from one prepared state (clone, at scale — see
  optional lab H).

## Clean up
```bash
kubectl ate delete actor buddy-2 -a workshop   # if it says "another operation is in progress", wait ~3s for idle-suspend to finish and retry
kubectl ate delete tag knows-pineapple -a workshop
```
(Delete the actor before the tag — an actor created from a tag borrows its snapshot until its own first
suspend, and substrate doesn't yet stop you from deleting a tag out from under a clone that still needs it.)

## Next
Back to [Module 7 — Wrap-up](../../modules/07-wrap-up/README.md) for the rest of the optional labs.
