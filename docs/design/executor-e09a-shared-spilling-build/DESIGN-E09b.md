# E-09b — load-once-per-batch (Variant B)

Status: accepted for implementation. Owner item: TODO_ALL E-09b. Sibling of
`DESIGN.md` (E-09a, Variant A), which this lands on top of and does not
change. PG oracle for the state machine: `postgres/src/backend/executor/
nodeHashjoin.c` (`PHJ_BATCH_LOAD` / `PHJ_BATCH_PROBE` / `PHJ_BATCH_SCAN` /
`PHJ_BATCH_FREE`, `ExecParallelHashJoinNewBatch`) and `nodeHash.c`
(`ExecParallelHashTableSetCurrentBatch`, `ExecHashTableDetachBatch`).

## 1. What is left after Variant A

E-09a removed the duplicated BUILD: the leader partitions the inner side once
and publishes an immutable descriptor (`nbatch`, `bucketBits`, `nbuckets`,
`buildIsLeft`, frozen inner files 1..n-1). What it did **not** remove is the
duplicated TABLE. Each participant still calls `loadInnerBatch`, opens its own
`spillReader` on the shared file, and inserts into its **own** fresh map. With
five participants, batch *k* is decoded five times and resident five times —
D-04's 5x memory multiplier, measured at a 506 MB live map on TPC-H Q9.

Variant A's own accounting says so: "Memory: N x one batch instead of N x the
whole build". E-09b makes it "1 x one batch", which is PG's shape.

## 2. What PG does, and what goopg needs from it

PG's parallel hash join runs every participant through a per-batch barrier:
`PHJ_BATCH_LOAD` (one participant, or several cooperatively, loads the inner
side into shared memory), `PHJ_BATCH_PROBE` (everyone probes the one shared
table), `PHJ_BATCH_FREE` (the last participant to detach frees the chunks —
`ExecHashTableDetachBatch`). The barrier exists because PG must also hand out
the *outer* side batch-wise from a shared tuplestore.

goopg needs only the middle third of that. As `DESIGN.md` §3 records, the
Gather partitions the **probe** by scan block: every participant owns a
disjoint outer slice, writes its own outer batch files, and walks batches
0..n-1 in ascending order on its own. So goopg needs

- **load once**: the first participant to arrive at batch *k* loads it; the
  others adopt the same maps;
- **free once**: the maps go when the last participant leaves batch *k*;

and it needs **no barrier at all**. No participant ever has to wait for
another participant to *arrive*; it only ever waits for a load already in
flight to *finish*. That distinction is the whole safety argument below.

## 3. Why sharing the reloaded map is as safe as sharing batch 0

Batch 0's maps have been shared across participants since P8. The rule that
makes it legal (`parallel_hash_build.go` header) is the ordinary Go one: a map
is safe for unlimited concurrent reads provided no writer runs concurrently.
A reloaded batch is the same object under the same rule — built by exactly one
goroutine, published with a happens-before edge, then read-only.

The two pieces of state that ride *beside* the map stay private, and both are
already per-`joinOp`:

- the matched bitmaps (`lazyMatchedS/I/Cur`) are separate maps keyed by the
  same key, not fields inside the shared bucket — but note they cannot occur
  here anyway: `hashJoinIsPartialCapable` admits only INNER, SEMI, ANTI and
  LEFT-with-probe-side-outer, and `fillBuildSide()` is false for all four;
- the NULL-key build rows (`fillNullBuild`) are a per-operator slice. A shared
  load must therefore *carry* them in the payload and let every participant
  append its own copy, rather than letting only the loader collect them.
  Under today's shareable set the slice is always empty (`recordBuildNullKey`
  no-ops when `!fillBuildSide()`); carrying it is what keeps the mechanism
  correct if the shareable set widens.

## 4. The batch state machine

Per shared batch *k* (1..nbatch-1) the descriptor holds at most one
`sharedBatchLoad`:

```
        (no entry)
             |  first arrival claims the slot   (acquireLoad -> mine=true)
             v
        LOADING  --- loader reads inner[k], inserts, publishes, close(done)
             |                                    ^
             |                                    | later arrivals join here
             |                                    | (acquireLoad -> mine=false,
             v                                    |  refs++, wait on done)
        LOADED (refs = number of holders)
             |
             |  every holder releases when it leaves batch k
             v
        FREED -> slot cleared; a LATE arrival re-loads from the file,
                 which is still linked (releaseSharedHashBuilds owns it).
```

`FREED -> (no entry)` rather than a terminal state is deliberate. Without a
barrier a straggler can reach batch *k* after everyone else has passed it, and
the file is the durable copy. The cost bound is what matters: a batch is loaded
at most once per participant, i.e. **the worst case is exactly Variant A's
load count**, and the expected case is one, because participants traverse
batches in the same ascending order at similar rates.

## 5. The cancellation protocol

This is the first real cross-worker wait in goopg's executor, so the rule is
stated as a property, not as a hope.

**Rule 1 — the loader never waits, and never observes cancellation.** Once a
participant claims a batch it runs the load to completion: a bounded local
file read plus map inserts, with no channel operation and no `ctx` check. This
is not a new liberty — Variant A's `loadInnerBatch` is already uninterruptible
in exactly the same way — and it is what makes the wait terminate. The loader's
completion depends on the filesystem alone, never on another participant, so
there is no cycle to deadlock in.

**Rule 2 — `done` is closed on every exit, panic included.** The loader
publishes through `defer close(ld.done)` with `ld.err` pre-set to a pessimistic
"abandoned" error that a successful load overwrites. A panicking loader
therefore hands every waiter an error rather than a channel that never closes.
This is precisely the failure mode plain `sync.Once` has and why it is not
used here: `Once.Do` marks the slot done when its function *returns*, so a
loader that returned early (cancelled, or having recovered a panic) would
publish an EMPTY map and every waiter would silently probe nothing — the
wrong-answer class this item is graded on. A `sync.Once` whose function
blocks forever hangs every waiter with no cancellation escape at all.

**Rule 3 — every wait is `ctx.Done()`-aware.** A waiter selects on
`ld.done` and on the participant's own `ctx.Ctx.Done()`, and on cancellation
returns the 57014 `ExecError` (via `lockWaitCancelError`, so a statement
timeout is reported as one) after dropping its reference. This is the
LIMIT-above-Gather case: `gatherOp.Close` cancels the group before draining,
so a worker parked on a load in flight leaves immediately instead of paying
for a batch it will never probe.

**Rule 4 — the reference is taken before the wait and dropped on every exit.**
`acquireLoad` increments `refs` under the descriptor mutex *before* returning,
so a load cannot be freed out from under a waiter that has not woken yet.
Every exit path drops it: cancellation, a load error, moving to the next batch,
`hashBatchState.close`, and the operator's error paths (which all funnel
through `releaseBatches` -> `close`). Release is idempotent — the holder's
pointer is nil'd as it is dropped.

**Rule 5 — file lifetime is unchanged.** `releaseSharedHashBuilds` still
unlinks each inner file exactly once, from the Gather/GatherMerge owner's
`Close`, after `group.Wait()`. Since the loader is part of the fan-out, no load
can still be reading at that point. The descriptor's `release` additionally
drops the load slots so the maps go with the publication.

### Why the operator's pointer is dropped before the reference

`releaseHeldBatch` nils `o.lazyHash`/`o.lazyIntHash` *first* and only then
decrements `refs`. If it did not, the participant would still be pointing at
batch *k*'s map while batch *k+1* loads, and peak memory would be two batches
per participant instead of one — the bug this item exists to avoid, in
miniature.

## 6. Instrumentation — the memory evidence

Counting is the evidence, because the whole claim is about an object count.
The descriptor keeps four numbers under the mutex it already takes:

| field | meaning |
|---|---|
| `loadCount` | loads actually run. Variant A's value is `participants x batches`; E-09b's floor is `batches`. |
| `maxLiveLoads` | high-water mark of simultaneously-resident batch tables. **This is the 5x -> 1x figure.** |
| `maxLiveBytes` | high-water mark of `spaceUsed` summed over live loads — the same figure in bytes. |
| `waiting` | participants currently parked on a load; used by the cancellation test to prove a waiter really was blocked. |

Retained-heap comparisons, if a profile is ever taken, use `inuse_space`;
allocation COUNT is the figure that matters in this codebase, and `loadCount`
is exactly that count for this mechanism.

## 7. Gate

E-09a's gate, unchanged and re-run, plus:

- **Protocol unit tests** on `acquireLoad`/`releaseLoad` directly, since they
  are the only deterministic way to pin a concurrency contract: N goroutines
  race for one batch -> exactly one loader, `loadCount == 1`,
  `maxLiveLoads == 1`, all N see the same map; all release -> slot cleared,
  maps dropped, `liveLoads == 0`; a late arrival re-loads.
- **Cancel-mid-batch (mandatory)**: a loader parked on a test channel with
  N-1 waiters blocked on `done`; cancel the context; every waiter returns
  57014 within the timeout; unblock the loader; no reference leaks. Plus the
  end-to-end arm E-09a already has (LIMIT above the Gather, cancelled from
  inside a batch-1 reload) re-run against the shared loader.
- **Panic-safety**: a loader that panics publishes an error; waiters get it
  instead of hanging.
- **Memory assertion**: through the operator-level harness with N
  participants over a batched shared build, `maxLiveLoads` is strictly less
  than N and `loadCount` strictly less than `N x batches` — the multiplier is
  gone; and in the rendezvous arm, exactly 1 and exactly `batches`.
- Forced-shape values tests per join type at batching `work_mem` (identity vs
  the serial join) — E-09a's, re-run, since a shared map that is adopted
  wrongly is a silently partial join.
- `go test -race ./internal/executor/` on the parallel-hash shapes.
- The E-09a exactly-once-open counter changes meaning and is restated, not
  deleted: the invariant is no longer "every participant opens every file
  once" (that is what E-09b removes) but "no participant opens a batch twice,
  and no batch is loaded more times than there are participants".

## 8. Size

~150-200 LOC in `join_batch.go` (the load slot, acquire/wait/release, the
shared arm of `loadInnerBatch`) plus small edits to `parallel_hash_build.go`
and the participant state constructor. Tests exceed it. Risk class is HIGH and
unchanged from E-09a: the failure is a silently partial join.
