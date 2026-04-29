# 0013-0002 — Overflow + Eviction Durability Ordering

**Status:** accepted
**Milestone:** [0013 — WAL Buffers Optimization](../milestones/0013-wal-buffers-optimization-with-eviction-safe-wal-before-data-durability.md)
**Spans seam:** WAL-before-data invariant under buffered WAL +
eviction / checkpoint flush paths.
**Cross-links:**
[0013-0001](0013-0001-wal-buffers-architecture.md) (the buffer + two-stage FlushUpTo),
[0002-0001](0002-0001-checkpointing.md) (checkpointer flush path),
[root-0008](root-0008-wal-and-recovery.md) (WAL writer baseline).

## Context

M0013-0001 introduced the bounded in-memory WAL buffer and gave
`FlushUpTo` two-stage semantics (drain through `targetLSN`, then
`dataSync`). The milestone's WAL-before-data guardrails (DoD #6 / #7)
require that every data-page writeback path — single-page eviction,
batched dirty-flush, checkpoint flush — drives WAL through the page's
LSN before the heap write hits disk.

The good news is that the existing storage layer already calls
`Writer.FlushUpTo(pageLSN)` at every flush site that matters. With
M0013-0001's two-stage upgrade, those calls **automatically** drain
the buffer through pageLSN before dataSync — the invariant is
preserved by construction. This slice's job is to **pin that
invariant with explicit regression tests** so a future refactor that
inadvertently bypasses the FlushUpTo, or stops draining the buffer
inside it, is caught immediately.

## Existing flush sites

`internal/storage/bufpool.go`:

| Site                   | Line | Behaviour                                      |
|------------------------|------|------------------------------------------------|
| `flushSlot`            | 886  | Single-page eviction / explicit flush.         |
| `flushDirtyBatch`      | 836  | Batched flush with combined `FlushUpTo(maxLSN)`. |

Both call `wal.FlushUpTo` before `mgr.WriteBlock`. The `wal` field
is the abstract `WALSyncer` interface defined at line 192:

```go
type WALSyncer interface {
    FlushUpTo(lsn uint64) error
}
```

A real `*wal.Writer` satisfies this interface; the existing
`recordingWAL` test stub also does. With M0013-0001 the real
implementation now drains the in-memory buffer first, then dataSyncs
— the same call site, the same contract.

## Test contract

Two integration tests in `internal/storage/wal_buffer_eviction_test.go`
wire a real `*wal.Writer` (with `WALBuffers > 0`) into a Pool and
prove the durability ordering survives the buffered path.

### TestEvictionDrainsBufferedWALBeforeWritingPage

Setup:
- Real `*wal.Writer` with `WALBuffers = 64 KiB` and a 4-KiB segment
  size, in a fresh tempdir.
- Pool with one slot wired to that writer.

Steps:
1. `Append` a small WAL record, capture its `endLSN`.
2. Confirm the segment file size is **0** (record is in walBuf, not
   on disk yet — the M0013-0001 invariant for small records under
   the buffer cap).
3. `PinNew` a heap page, mutate one byte, `MarkDirtyWithLSN(endLSN)`,
   `Unpin`.
4. Force eviction with a second `PinNew` on the one-slot pool.
5. After the second `PinNew` returns:
   - The heap-rel file must contain the mutated byte (eviction
     flushed the page).
   - The WAL segment file size must be **non-zero** — Stage 1 drain
     pushed the buffered record to disk.
   - `wal.ReadAll(walDir)` must include the appended record's
     payload.

If anyone disables Stage 1 (drops `drainBufferUpTo` from
`flushUpTo`), the heap byte survives but the WAL segment is empty
— a recovery would lose the record while the heap mutation persists.
This test catches that regression by asserting both invariants
together.

### TestFlushAllPacedDrainsBufferedWAL

Same setup but exercises the batched flush path (`flushDirtyBatch`):
1. Mutate a few pages with different LSNs (all in walBuf range).
2. Call `Pool.FlushAllPaced` (the checkpointer-driven entry point).
3. Assert all WAL segments contain the records (Stage 1 ran on the
   batched `FlushUpTo(maxLSN)`).
4. Assert all heap pages persist (Stage 2 ran).

Pins the M0013-0001 two-stage contract for the
checkpoint-driven flush path.

## Failure mode catalogue

| Bug                                            | Test that catches it                              |
|------------------------------------------------|---------------------------------------------------|
| Stage 1 (drainBufferUpTo) skipped               | `TestEvictionDrainsBufferedWALBeforeWritingPage` (segment file 0 bytes after eviction). |
| Stage 2 (dataSync) skipped                      | Already covered by `TestEvictionFlushesWALBeforeData` (existing call-order test). |
| Eviction calls `WriteBlock` before FlushUpTo    | Existing `recordingWAL` ordering test.            |
| Batched flush passes stale maxLSN to FlushUpTo  | `TestFlushAllPacedDrainsBufferedWAL` (per-page records absent from segments). |

## Out of scope

- Counter / observability surface for forced-drain events
  (`forced_drains_for_eviction`) — M0013-0003.
- Performance optimisation of the drain hot path (e.g. coalescing
  across nearby LSNs) — separate scope.
- Crash-recovery testing under a buffer full of un-drained records
  — by design those records are lost; the existing
  `TestPreallocatedSegmentRecoversCleanly` already covers
  recovery from segment files only.

## Implementation effort

Pure additive — no production code changes. The M0013-0001 commit
already wired the two-stage FlushUpTo through the existing flush
sites. This slice adds tests that pin the contract.
