# 0107-0007g — Heap-insert call site: adaptive batched-extend tail

Milestone: M0107-0007 (Phase D4: WAL insert striping + FSM page
distribution) — slice C call-site rewrite (part 2 of 2).

Parent design: `docs/design/perf-optimize/07-wal-fsm-insert.md` §3
(FSM-driven page selection).

## Why

Slice C part 1 ([[0107-0007f]]) wired `selectFSMCandidatePage` into the
pre-lock FSM consultation, biasing each backend away from the hot tail
page when there are FSM candidates with low pin counts. What remained
on the hot path is the *extension* tail itself: once the FSM and the
existing tail block both reject the insert, the previous code called
`Pool.PinNew(rel)` which appends exactly one page per call. Under
contention from N stripes that all miss the FSM at the same instant,
each stripe still extends one page and the new tails immediately
re-contend — the FSM never gets a chance to populate.

The parent §3 algorithm prescribes:

1. After taking the extend lock, re-consult the FSM (a sibling stripe
   may have just batch-extended and registered fresh candidates).
2. If still missing, batch-extend `extendBatchSize = 8` pages in one
   smgr-level call, FSM-register the extras, and insert into the first
   new block. Subsequent stripes hit the cross-stripe FSM re-check and
   land on the extras instead of extending.

This loop implements both steps, but conditions the batched-extend on
actual lock-acquisition contention so an uncontended single-INSERT
keeps the one-page-per-event semantics the rest of the codebase
(VACUUM all-visible expectations, HOT-update page-count invariants,
LockRows ItemPointer expectations) was built against.

## Design choice: adaptive batching (TryLock-as-proxy)

PG's `RelationGetBufferForTuple` (`postgres/src/backend/access/heap/hio.c`)
caps the batch size on `RelationExtensionLockWaiterCount`:

```c
extraBlocks = Min(512, lockWaiters * 20);
```

That is, PG only batch-extends when there is observable wait pressure on
the relation extension lock. We approximate the same heuristic without
a waiter-count counter:

- `lockHeapExtend(rel, procNum)` now attempts `TryLock` first; if it
  succeeds, the lock was uncontended and we return `contended=false`.
  On `TryLock` failure we fall through to the blocking `Lock()` and
  return `contended=true`.
- The two heap-insert call sites use `contended` as the batch-size
  selector: `true` → `batchExtendAndRegisterFSM` (extendBatchSize=8 +
  FSM registration of seven extras); `false` → the original PinNew
  one-page fast path.

This keeps the fast path identical for every test that was originally
written against single-INSERT semantics (a fresh relation grows to
exactly one page after the first row, VACUUM sees one page, etc.) and
only enables batching when the lock acquisition observed a peer
stripe-mate already in the extension critical section — a proxy for
"under load." Crucially, the TryLock probe is cheap (one atomic CAS on
the success path) so the proxy does not bleed into the uncontended
fast path's latency.

## What changed

`internal/executor/heap_insert_select.go`:

- New constant `extendBatchSize = 8` (matches parent §3).
- New helper `batchExtendAndRegisterFSM(pool, fsm, rel)`:
    1. Calls `Pool.ExtendRelationBatch(rel, extendBatchSize)` — one
       smgr-level acquire + one `WriteAt(8*BlockSize)`.
    2. FSM-registers blocks `[firstBlk+1 .. firstBlk+extendBatchSize-1]`
       at empty-page free space (`BlockSize - SizeOfPageHeaderData`).
       The constant is used directly rather than reading each page
       back through the buffer pool, because `ExtendRelationBatch`'s
       pre-initialised buffer makes the layout deterministic.
    3. Returns `firstBlk`. It is intentionally NOT FSM-registered here:
       the caller's normal `markHeapInsertDirty →
       FSM.RecordFreeSpaceForPage` path will record the post-insert
       free space for `firstBlk`, identical to the pre-refactor path.
- Nil-FSM safe: registration is skipped if `fsm == nil`.

`internal/executor/operators_storage.go`:

- `lockHeapExtend(rel, procNum)` signature changes from
  `func() unlock` to `(unlock, contended bool)`. Implementation now
  `TryLock`s first and signals contention via the return.
- `writeHeapRowReturning` and `writeHeapRowReturningPG`: after taking
  the extend lock, re-consult the FSM via `selectFSMCandidatePage`
  (cross-stripe pickup), then re-check the tail block, then branch on
  `contended`:
  - `contended` → `batchExtendAndRegisterFSM` +
    `tryAppendToBlock(firstBlk)`.
  - `!contended` → original `Pool.PinNew(rel)` + `PageAddHeapTuple` +
    `markHeapInsertDirty` + FSM/VM bookkeeping.

`internal/executor/extend_lock_stripe_test.go`: the two pre-existing
test callers of `lockHeapExtend` updated for the new signature, plus
two new assertions that pin the contended/uncontended contract (first
acquirer of a stripe sees `contended==false`; a second acquirer that
had to block sees `contended==true`).

`internal/executor/heap_insert_select_test.go`: three new tests for
`batchExtendAndRegisterFSM` (next section).

## Tests

Six existing `TestSelectFSMCandidatePage*` tests in
`internal/executor/heap_insert_select_test.go` continue to PASS unchanged
— the helper landed in part 1 ([[0107-0007e]]).

Three new tests pin the batched-extend half:

- `TestBatchExtendAndRegisterFSMAppendsAndRegistersExtras`: empty
  relation → one batch-extend call extends to NBlocks=8; FSM-drained
  set equals `{1..7}`; `firstBlk=0` is *not* registered; total count is
  `extendBatchSize-1`.
- `TestBatchExtendAndRegisterFSMNilFSM`: extension still runs, no
  panic, no registration side effect (nil-safety contract).
- `TestBatchExtendAndRegisterFSMSecondCallContinuesAndRegisters`: two
  successive batches stay disjoint (second `firstBlk = extendBatchSize`,
  NBlocks=16), and the FSM accumulates both batches' extras (14
  entries, no overlap, neither `firstBlk` leaked into the FSM).

Updated `extend_lock_stripe_test.go` adds assertions that the first
acquirer of a stripe observes `contended==false` and that a peer
acquirer of the same stripe (which must wait) observes
`contended==true`.

Verified:

- `go test -race -count=1 ./internal/executor/` (2.76 s) — PASS.
  Exercises every existing path through `writeHeapRowReturning` /
  `writeHeapRowReturningPG`: HOT updates, VACUUM/VM, LockRows
  (FOR UPDATE/FOR SHARE), partition routing, upsert, merge, apply
  worker.
- `go test -race -count=1 ./internal/storage/` (5.37 s) — PASS.
- `go test -race -count=1 ./internal/wal/` (3.23 s) — PASS.
- `go test -race -count=1 ./internal/server/` (5.83 s) — PASS.

## Why this is byte-safe for WAL replay

Per-record WAL emission is unchanged. The two on-disk side effects
that *do* differ when `contended==true` are:

1. **Relation file grows by 8 blocks instead of 1.** This is purely
   smgr-level: `ExtendRelationBatch` writes 8 pre-initialised pages in
   one `WriteAt`. No WAL record describes "the relation grew" in
   either PG or goopg — PG infers relation size from later WAL records
   that reference the new blocks (or from on-disk size during recovery
   from a base backup + WAL replay). On the standby, the relation
   grows to cover whatever block any replayed record references; the
   extra empty pages on the primary that are never WAL-touched simply
   never appear on the standby. The on-disk divergence is identical
   to PG's own batched-extend behaviour and is part of the recovery
   model.
2. **`SmgrCreate` WAL emission.** Both `Pool.PinNew` and
   `Pool.ExtendRelationBatch` emit exactly one `SmgrCreate` record
   per relation, gated on `firstBlk == 0`. The emission is
   byte-identical and the gating is identical.

The WAL byte-diff integration gate from the parent milestone (parent
§verification, pre/post-D4 WAL segment diff modulo timestamps for a
fixed pgbench workload) covers the combined slice A + slice C effect
on per-record WAL output. That gate is the parent milestone's
acceptance criterion and will fire once the slice B work (parent §2 —
`wal.Writer.appendLocks` striping) lands.

## What's intentionally not in this loop

- **Slice B — WAL insert striping (parent §2):** splitting
  `state.appendMu`'s four invariants (writePos, walBuf state, memRing
  append, writeLSN advance) into per-stripe local + shared state plus
  atomic `nextLSN.Add` and a dedicated `rotateMu` is multi-loop scope.
  It remains the last outstanding piece of M0107-0007 before the
  pgbench c=100 SU TPS ≥ 500 gate can be evaluated.
- **Adaptive batch-size scaling:** PG's `Min(512, waiters * 20)`
  heuristic uses an actual waiter count to scale up further than 8 at
  very high concurrency. Our binary `0 vs 8` proxy is good enough at
  c=100 (eight stripes saturate the eight-page batch), but tuning the
  upper bound is future work — it would require an explicit waiter
  counter on `paddedMutex`, which `sync.Mutex` does not expose.
- **TOAST relation insert path:** `ToastLargeColumnsIfNeeded` recurses
  into the TOAST chunk relation via the same `writeHeapRowReturning`
  function, so it inherits the adaptive tail automatically — no
  separate change is needed.

## Related

- [[0107-0007a]] — 8-stripe heap-extend lock (slice A); the
  `lockHeapExtend` signature change for `contended` lives here.
- [[0107-0007b]] — `FSM.GetCandidates` top-K primitive.
- [[0107-0007c]] — `Pool.ExtendRelationBatch` batched-extend
  primitive (consumed by this loop).
- [[0107-0007d]] — `Pool.SlotPinCount` lock-free pin-count probe.
- [[0107-0007e]] — `selectFSMCandidatePage` selection helper
  (consumed by both pre-lock and post-lock FSM consultations).
- [[0107-0007f]] — slice C call-site rewrite part 1 (pin-aware
  pre-lock FSM consultation).
