# 0107-0007c — `Pool.ExtendRelationBatch` (slice C foundation 2 of 3)

Status: accepted (2026-05-21)
Parent: [`docs/design/perf-optimize/07-wal-fsm-insert.md`](../../perf-optimize/07-wal-fsm-insert.md)
Sibling: [`0107-0007a-heap-extend-lock-striping.md`](0107-0007a-heap-extend-lock-striping.md),
[`0107-0007b-fsm-get-candidates.md`](0107-0007b-fsm-get-candidates.md)

## What

Add `Pool.ExtendRelationBatch(rel, n) (firstBlk, error)`: appends `n`
contiguous empty pages to `rel` in a single batched `WriteAt` and returns
the block number of the first new block. Pages are written through
`Manager.ExtendBatch` (new), itself a thin wrapper over a new
`relFile.extendBatch` that holds `relFile.mu` once across the whole
batch.

Unlike `Pool.PinNew`, no buffer slot is pinned and no `bufmap` entry is
published — the new pages live on disk only. The heap-insert caller in
[`07-wal-fsm-insert.md`](../../perf-optimize/07-wal-fsm-insert.md) §3 then
registers blocks `firstBlk+1 .. firstBlk+n-1` in FSM via
`fsm.RecordFreeSpace`, and uses `firstBlk` for its own insert (pinning it
the normal way via `Pool.Pin`).

The `SmgrCreate` WAL record fires exactly once, only when the batch
includes block 0 (i.e. relation transitions from empty to non-empty),
matching the invariant pinned by `PinNew`.

## Why

The parent chapter rewrites `selectInsertPage` to allocate multiple
pages per extension event so concurrent inserters distribute across the
new pages via FSM, breaking the tail-page hot-spot. `Pool.PinNew` extends
one page per call and pins it for the caller; that shape is wrong for a
batched FSM-driven workflow — the caller needs the page numbers, not
buffer slots, and the FSM registration is the right way to expose the
extras.

The smgr-level batching matters because eight 8-page extensions across
distinct stripes (per [`0107-0007a`](0107-0007a-heap-extend-lock-striping.md))
become 64 page-writes per relation per burst; replacing 64 syscalls with 8
(one per batch) is the dominant disk-side improvement.

## How (signatures)

```go
// internal/storage/smgr.go
func (m *Manager) ExtendBatch(rel RelFileNode, buf []byte, n int) (BlockNumber, error)

// internal/storage/bufpool.go
func (p *Pool) ExtendRelationBatch(rel RelFileNode, n int) (BlockNumber, error)
```

`ExtendBatch` requires `buf` to be the BlockSize-sized initialized page
that gets copied N times into the on-disk extent. `ExtendRelationBatch`
allocates and initializes that buffer internally so callers don't need to
import `InitPage`.

## Lock discipline

- `Manager.ExtendBatch` calls `OnExtendWait` / `OnExtendDone` once for
  the entire batch (matches the per-call semantics of `Manager.Extend`;
  the activity-registry observer sees one DataFileExtend wait event per
  batch, not N).
- `relFile.extendBatch` holds `r.mu` for the whole batch — one
  `WriteAt(n*BlockSize)` issues a single contiguous write at the
  pre-batch tail offset, then `r.nblocks += n` publishes the new size.
- No interaction with `Pool.pinMu` or `bufmap`. Subsequent pinners of
  any block in the new range take the normal `Pool.Pin` path, which
  observes the freshly-grown file via the next `mgr.ReadBlock`.

## What ships in this slice

This is **foundation 2 of 3** for M0107-0007 slice C. The executor
consumer (`selectInsertPage`) lands after foundation 3 (`Pool.SlotPinCount`,
blocked on M0107-0006's lock-free `bufmap`). Foundation 1
(`FSM.GetCandidates`) shipped in [`0107-0007b`](0107-0007b-fsm-get-candidates.md).

## Tests

`internal/storage/storage_test.go`:

- `TestExtendRelationBatchAppendsContiguousBlocks` — 8-block batch yields
  `firstBlk=0` and `NBlocks=8`; each block's on-disk image equals
  `InitPage(buf)`; a follow-up 4-block batch starts at block 8 and grows
  `NBlocks` to 12.
- `TestExtendRelationBatchEmitsSmgrCreateOnceOnFirstBatch` — first batch
  (firstBlk=0) emits `LogSmgrCreate` exactly once; second batch
  (firstBlk>0) emits nothing.
- `TestExtendRelationBatchInteropWithPinAndExtend` — interleaves
  `PinNew → ExtendRelationBatch → PinNew`; verifies that batch-added
  blocks Pin cleanly and that headers (`Lower`, `Upper`) are the
  expected `InitPage` defaults.
- `TestExtendRelationBatchRejectsNonPositiveN` — `n ∈ {0, -1, -8}`
  rejects with an error and `NBlocks` stays 0.

`go test -race -count=1 ./internal/storage/` PASS (5.36 s).

## PG counterpart

PG's `ExtendBufferedRelTo(rel, num_pages, …)` (file:
`postgres/src/backend/storage/buffer/bufmgr.c`) does the equivalent
batched extension; it differs in that PG returns buffer descriptors for
each new page (PG's hio.c pins all N), whereas goopg's
`ExtendRelationBatch` keeps the pages unpinned and lets the next
inserter find them via FSM. This is intentional: the goopg path uses an
8-stripe extend lock (per [`0107-0007a`](0107-0007a-heap-extend-lock-striping.md))
plus pin-count-aware page selection (per parent §3); batch-pinning all
N would defeat the pin-count distribution by registering every batch
victim as pinned at allocation time.

## Out of scope

- `Pool.SlotPinCount(tag)` — foundation 3, blocked on M0107-0006's
  lock-free `bufmap`.
- `selectInsertPage` rewrite (parent chapter §3) — consumes foundations
  1, 2, 3; lands once all three are in.
- WAL insert striping (parent §2) — separate slice B, multi-loop scope.
