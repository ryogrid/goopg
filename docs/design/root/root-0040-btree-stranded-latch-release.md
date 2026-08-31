# root-0040 — The regress-suite wedge was a page latch stranded by a recovered panic

status: accepted
date: 2026-08-06
task: M-NIGHTLY `regress/suite-wedge` (AI-20260806-191958-010 and its
predecessors on `aggregates` / `jsonb` / `misc` / `multirangetypes`)
code: `internal/access/btree/latch_release.go`,
`internal/access/btree/btree.go` (`insertIntoBlock`),
`internal/access/btree/latch_release_test.go`

## 1. What was open

Nine loops of M-NIGHTLY triage had established what the wedge is *not*, and the
item still read **"STILL OPEN: what wedges the cluster."** Established before
this loop:

- one regress case per run stops dead and dies on the harness's 120 s per-case
  deadline, while **its own 5 s `statement_timeout` passes unobserved**;
- it is **not** host overload and **not** a GC-thrashing server: the wedged
  run's other 231 cases summed to 178.5 s, *faster* than the same suite with no
  wedge at all;
- the wedge **moves** between runs, so it is not a property of any case;
- the case's 8 "casualties" were the recovery path doubling every fixture table
  (fixed the previous loop).

## 2. What actually happens

A live specimen answered it. An orphaned `goopg` server left by a regress-suite
run was still resident, burning CPU, refusing to exit, with its data directory
already deleted. Its goroutine dump (`analysis/m0127-s7-regress/orphan-3051493/`)
showed:

```
goroutine 56 [sync.RWMutex.RLock, 10 minutes]:
  storage.(*Pool).flushBatch          bufpool.go:2318
  storage.(*Pool).FlushAllPaced/FlushAll
  wal.(*Checkpointer).flushDirty → runCheckpoint → Run
```

The checkpointer had been blocked ten minutes on one slot's **shared** content
latch — and **no goroutine in the process held or wanted that latch**. A latch
with no owner alive means one thing: it was acquired and never released by a
goroutine that no longer exists.

That server's log named the goroutine. At `22:14:14.871` — the same minute the
checkpointer blocked — it recorded:

```
level=ERROR msg="backend goroutine panic"
panic="runtime error: storage: not enough free space in page"
  btree.insertItemSorted                 btree.go:2854
  btree.(*BTree).insertIntoBlock         btree.go:2392
  btree.(*BTree).Insert                  btree.go:2150
  executor.maintainUniqueIndexesForInsert
  executor.(*updateOp).Next
```

`insertIntoBlock` takes the leaf's **exclusive** latch through `pinW` and
releases it with an explicit `bt.unpinW(slot)` — not a `defer`, because the
split path hands latches between frames. That is correct for every `return` and
wrong for a **panic**: `internal/server`'s per-connection handler recovers every
backend panic (`server.go:~799`) so one bad statement cannot kill the
postmaster, so the goroutine holding the latch simply vanishes and
`sync.RWMutex` has no owner left to release it.

Every part of the wedge signature follows from that one stranded latch:

| observation | why |
|---|---|
| a statement hangs past its own `statement_timeout` | it is parked in `contentMu`, and a mutex wait observes no deadline — nothing checks the statement clock |
| the server stays fast otherwise | only queries touching that one page block |
| the wedge case moves run to run | which case wedges depends solely on who next touches the poisoned page |
| the server ignores SIGTERM and orphans | the shutdown checkpoint's `FlushAll` wants the same latch shared — this is the "testport orphan crawl" |

PostgreSQL cannot reach this state: `elog(ERROR)` longjmps to the abort path and
`AbortTransaction()` calls `LWLockReleaseAll()`
(`postgres/src/backend/storage/lmgr/lwlock.c`,
`postgres/src/backend/access/transam/xact.c`), dropping every content lock the
backend held. goopg has **no equivalent** — that is the underlying gap.

## 3. The fix

`insertIntoBlock` — the path the wedge was observed on — now owns its latch
through a `wlatch` holder whose `release()` is **idempotent**, so the function
keeps calling it on all eleven of its normal exit paths *and* defers it for the
panic path:

```go
held := wlatch{bt: bt}
defer held.release()          // no-op on every normal return
...
slot, err = bt.pinW(blk); held.hold(slot)
```

The panic is deliberately **not** recovered. It keeps propagating to the
connection handler with its original stack, so a genuine bug stays as loud as it
was; this only stops one statement's bug from poisoning the whole cluster.

### 3.1 A rejected design, and why it matters

The first cut kept the held latches in a registry on the `*BTree` and swept it
at every public entry point — the closer shape of `LWLockReleaseAll`. It is
**unsound here**, and the package's own tests proved it twice:

1. `*BTree` is **not goroutine-private** (`TestConcurrentSearchAfterInserts`
   drives concurrent `Search`es through one instance), so one goroutine's sweep
   releases another goroutine's latches — the whole package deadlocked;
2. callers may take a latch through `pinW`/`pinR` outside any entry point
   (`lpdead_kill_test.go:225` does), and a sweep double-releases it.

A per-goroutine registry would need a goroutine id on the descent hot path, and
`runtime.Stack` on a hot path is a mistake this repo has already paid for
(`m0107_gc_hotpath_fix`, `perf_optimize2_stripenum_runtime_stack`). The local
holder has none of these problems: it is a plain stack variable, private to the
frame that owns the latch, with zero cross-goroutine state and no hot-path cost.

## 4. What this does NOT fix

- **The trigger.** `pageHasSpaceFor` said the item fit and `insertItemSorted`
  then panicked "not enough free space in page" — the space check and the writer
  disagree. Filed as its own fix_plan item + ledger row. After this change that
  disagreement surfaces as one failed statement instead of a wedged cluster.
- **The general gap.** The descent's shared latches, the split path's
  `rightSlot`/`sibSlot` (locked-return contract, released by direct `Unlock()`),
  and every non-btree `Slot.Lock()` site in `internal/executor`
  (`sys_catalog_*`, `toast`, sequences) have the identical exposure. A real
  `LWLockReleaseAll` needs latch ownership plumbed through `Context` and is a
  milestone of its own. Ledger rows record both.

## 5. Verification

- `TestStrandedLatchReleasedOnPanic` — fills a leaf so `Insert` reaches the
  split path, injects a panic inside the leaf-write window via
  `panicBeforeLeafWrite`, asserts the panic propagates, then proves the tree is
  still usable from another goroutine within 20 s. **Non-vacuous**: with only
  the `defer held.release()` line removed the test does not pass — it hangs
  (120 s kill), which is the wedge reproduced in miniature.
- `TestWlatchReleaseIsIdempotent` — the property the deferred release depends
  on; triple `release()` then a fresh exclusive acquire that must not block.
- Full `internal/access/btree` package: PASS (2.5 s).
- The 11 converted `bt.unpinW(slot)` sites were rewritten mechanically within
  `insertIntoBlock`'s line range only; `sibSlot`'s two hand-written
  `Unlock()`+`Unpin()` pairs became `bt.unpinW(sibSlot)`, which also restores
  the `DebugTraceContentMu` bookkeeping those two sites were skipping.
