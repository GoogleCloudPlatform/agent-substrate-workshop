# Optional lab A — Tags, clone, revert (20 min)

Starts from the end of Module 4 or later (`make catch-up MODULE=5` gets you a `buddy` actor on `agent-v3`
if you skipped ahead).

A **tag** takes its own copy of an actor's external snapshot. Suspending the actor again — or deleting it —
can't touch what the tag copied. You can create a new actor from a tag (**clone**) or send a diverged actor
back to its own last snapshot (**revert**).

> **Fact-checked against the substrate source** (`cmd/kubectl-ate/internal/cmd/tag.go`,
> `revert_actor.go`, `actor.go` at `SUBSTRATE_REF`): `create tag` takes `--actor` and `-a/--atespace`, not a
> content scope — `--scope` here means the *tag's* visibility (`atespace` or `published`), not the
> FULL/DATA snapshot content scope you set on a template. And `revert actor` takes **no `--tag` flag at
> all**: it sends a crashed, running, or paused actor back to **its own last external snapshot**, discarding
> only the execution since then. See "What to notice" below for why that's actually the more useful demo.

```bash
make forward-gateway      # never hand-roll `kubectl port-forward &`; this wraps scripts/lib.sh's port_forward
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

# Same past, same bootID: buddy-2 hasn't suspended on its own yet, so it's running the exact FULL
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
```

## What to notice
- **Tag scope vs. snapshot content scope are different knobs.** `--scope atespace|published` on a tag
  controls who can read it (its own atespace, or any atespace via a published reference) — it does not
  choose FULL vs. DATA. Content scope is set once, on the template, via `snapshotsConfig.onCommit`
  (`agent-v3` uses `SNAPSHOT_CONTENT_SCOPE_FULL`, from Module 4).
- **A clone shares its past with the source** (same `bootID`, same conversation), exactly like the
  golden-snapshot callout in Module 5 — except here the shared point is whatever moment you tagged, not the
  template's golden snapshot. Anything that must be unique per actor (generated IDs, seeded randomness,
  fetched credentials) has to be regenerated on the first request after the clone starts, not at boot.
- **Revert is not "go to this tag."** It has no target snapshot argument — it discards a crashed, running, or
  paused actor's current execution and drops it back to SUSPENDED against whatever external snapshot it
  already has. That's still a real "undo," just scoped to "since the last suspend," not to an arbitrary
  point in history. To revert to an arbitrary earlier point, delete the actor and clone a fresh one from a
  tag instead — which is exactly what the clone step above did.
- Revert rolls back **everything in the snapshot's scope**, including `/data` for a `DATA`-scope template.
- Use cases: checkpoints before risky tool calls, forking a conversation (clone), an "undo" for the last
  turn or tool call (revert), and warm-starting many actors from one prepared state (clone, at scale — see
  optional lab E).

## Clean up
```bash
kubectl ate delete actor buddy-2 -a workshop --any-state
kubectl ate delete tag knows-pineapple -a workshop
```
(Delete the actor before the tag — an actor created from a tag borrows its snapshot until its own first
suspend, and substrate doesn't yet stop you from deleting a tag out from under a clone that still needs it.)

## Next
Back to [Module 7 — Wrap-up](../../modules/07-wrap-up/README.md) for the rest of the optional labs.
