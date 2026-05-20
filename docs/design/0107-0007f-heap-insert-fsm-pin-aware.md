# 0107-0007f — Heap-insert call site: pin-aware FSM consultation

Milestone: M0107-0007 (Phase D4: WAL insert striping + FSM page
distribution) — slice C call-site rewrite (part 1 of 2).

Parent design: `docs/design/perf-optimize/07-wal-fsm-insert.md` §3
(FSM-driven page selection).

## Why

Slice C foundations 1–3 landed the read-only primitives
(`FSM.GetCandidates`, `Pool.ExtendRelationBatch`, `Pool.SlotPinCount`).
Slice C executor foundation ([[0107-0007e]]) combined two of them into
`selectFSMCandidatePage`, a pure helper that picks the lowest-pin page
among the FSM top-K — but the helper had no callers yet. This loop
wires the helper into the actual heap-insert hot path.

The current FSM consultation in `writeHeapRowReturning` /
`writeHeapRowReturningPG` (`internal/executor/operators_storage.go`)
calls `ctx.FSM.GetPageWithFreeSpace(rel, minFreeBytes)`, which returns
the first page in free-space-desc order. Under c=100 pgbench writes,
every backend gets the same answer and converges on the same page;
the per-slot content lock serialises them.

`selectFSMCandidatePage` has the identical `(BlockNumber, bool)`
signature but additionally reads `Pool.SlotPinCount` for each top-K
candidate and short-circuits on pin == 0, biasing each backend toward
a page that nobody else is touching. When every candidate is at or
above `hotPinThreshold = 4` it returns `(0, false)` so the caller
falls through to the relation-extend path instead of joining the
contended queue. The matching-signature shape makes the wiring a
one-liner per call site.

## What changed

`internal/executor/operators_storage.go`:

- `writeHeapRowReturning`: the FSM-consult branch now calls
  `selectFSMCandidatePage(ctx.FSM, ctx.Pool, rel, minFreeBytes)`
  instead of `ctx.FSM.GetPageWithFreeSpace(rel, minFreeBytes)`. The
  helper's nil-safety covers the previous `ctx.FSM != nil` guard
  (it returns `(0, false)` on nil FSM or nil Pool), so the surrounding
  `if ctx.FSM != nil` wrapper is gone too.
- `writeHeapRowReturningPG`: identical swap, for the PG-canonical
  encoder path used by `LogCanonical`-enabled inserts.

Everything else in the heap-insert flow is unchanged:

- `minFreeBytes = len(tupleBytes) + 4` (4 = itemIDSize line-pointer
  size) is still computed the same way.
- `tryAppendToBlock(blk)` (the FSM-stale invalidation, `PageAddItem`
  retry, `markHeapInsertDirty` WAL emission) is byte-identical.
- The tail-page fallback (`nBlocks - 1`), the per-relation 8-stripe
  heap-extend lock from slice A ([[0107-0007a]]), and the eventual
  `PinNew` extension all stay put.

## Why this is byte-safe for WAL replay

Each insert still produces exactly one `XLOG_HEAP_INSERT` record with
the same payload encoding. The only thing that changes is *which*
existing page receives the tuple when multiple FSM candidates
qualify:

- Under low concurrency (every candidate at pin 0), `selectFSMCandidatePage`
  short-circuits on the first pin-0 candidate. FSM `GetCandidates`
  scans in ascending block-number order among same-free-space entries
  (see [[0107-0007b]]); `GetPageWithFreeSpace` returns the first
  free-space-desc match. For a single FSM entry the two are
  equivalent; for ties on free space, `GetCandidates` returns the
  lower block first, which matches `GetPageWithFreeSpace`'s scan
  order — so the selected block is unchanged in the no-contention
  case.
- Under high concurrency (the case this slice exists to fix), the
  selected block diverges deliberately. The WAL record format and
  per-record bytes are identical; only the block-reference number
  embedded in the record changes. PG standby replay treats each
  record independently — there is no "expected next block" rule
  that this could break.

The parent milestone's full WAL byte-diff integration gate (parent
§verification) covers the combined effect of slices A + B + C and
fires once the batched-extend half of slice C lands.

## What's intentionally not in this loop

- **Batched extension** — the design's §3 step 6 replaces `PinNew`
  with `Pool.ExtendRelationBatch(rel, extendBatchSize=8)` + FSM
  registration for the extras. That changes the at-rest disk-file
  growth rate for hot relations and needs the parent milestone's
  byte-diff gate to clear; it lands in the next slice C loop.
- **WAL insert striping (parent §2)** — `wal.Writer.appendMu` →
  8-stripe `appendLocks` plus atomic `nextLSN` with `rotateMu`
  remains slice B; splitting `state.appendMu`'s four invariants
  (writePos, walBuf state, memRing append, writeLSN advance) is
  multi-loop scope.

## Tests

The pin-aware ranking is already covered by six unit tests against a
real `Manager`+`Pool`+`FSM` fixture
(`internal/executor/heap_insert_select_test.go`, landed in slice C
foundation e — [[0107-0007e]]). The wiring change is a
matching-signature swap, so:

- `go test -race -count=1 ./internal/executor/` (2.76 s) — PASS.
  Exercises every existing path through `writeHeapRowReturning` /
  `writeHeapRowReturningPG` (planner integration, lockrows,
  apply-worker, upsert, merge, partition tests).
- `go test -race -count=1 ./internal/storage/` (5.38 s) — PASS.
  Pins the slice-C foundations the wiring depends on.
- `go test -race -count=1 ./internal/wal/` (3.26 s) — PASS. Confirms
  the WAL emission side is unaffected by the FSM-target change.
- `go test -race -count=1 ./internal/server/` (5.83 s) — PASS.
  Server-level coverage including PG-compat paths.

## Out-of-scope cleanup

The `tryAppendToBlock(blk storage.BlockNumber) (bool, error)` closure
is duplicated between `writeHeapRowReturning` and
`writeHeapRowReturningPG`; the two bodies are byte-identical apart
from their callers' encoder choice (`EncodeRow` vs `EncodeRowPG` —
both produce `tupleBytes` before the closure is built). De-duplicating
into a top-level helper is a future cleanup; the current loop is the
minimum surface for the slice-C wiring change.

## Related

- [[0107-0007a]] — 8-stripe heap-extend lock (slice A; the
  `lockHeapExtend` call already in place around the extension path).
- [[0107-0007b]] — `FSM.GetCandidates` top-K primitive.
- [[0107-0007c]] — `Pool.ExtendRelationBatch` batched-extend
  primitive (consumed by the next slice C loop).
- [[0107-0007d]] — `Pool.SlotPinCount` lock-free pin-count probe.
- [[0107-0007e]] — `selectFSMCandidatePage` selection helper.
