# 0013-0001 — WAL Buffers Architecture

**Status:** accepted
**Milestone:** [0013 — WAL Buffers Optimization](../milestones/0013-wal-buffers-optimization-with-eviction-safe-wal-before-data-durability.md)
**Spans seam:** in-memory WAL buffer, append-path overflow drain,
FlushUpTo two-stage semantics.
**Cross-links:**
[root-0008](root-0008-wal-and-recovery.md) (WAL writer baseline),
[0007-0002](0007-0002-fdatasync-commit-path.md) (durability barrier),
[0010-0002](0010-0002-walsender-in-memory-wal-handoff.md) (the
walsender-side mem ring; orthogonal to this milestone — same Append
hook will keep mirroring into both buffers).

## Context

Every `wal.Writer.Append` today calls `state.writeAt` immediately,
issuing a `pwrite` (or O_DIRECT RMW) per record. On write-heavy
workloads this dominates the WAL hot path. PostgreSQL avoids it via
the `wal_buffers` shared-memory ring: records first land in RAM and
are flushed to segment files only when `wal_buffers` overflows or a
durability barrier (commit, eviction, checkpoint) demands it.

This slice introduces the bounded in-memory WAL buffer and rewires
`Append` / `FlushUpTo` around it without touching the on-disk
segment format or WAL-before-data invariants. M0013-0002 covers the
eviction-driven drain path and M0013-0003 the observability surface.

## Buffer shape

```go
// internal/wal/wal_buffer.go
type walBuffer struct {
    cap     int64   // wal_buffers GUC value, fixed at construction
    buf     []byte  // ring backing storage; len == cap
    head    int64   // first un-drained byte offset (relative to base)
    tail    int64   // first unwritten byte offset
    base    uint64  // absolute LSN that buf[0] represents
}
```

- `tail - head` is the resident byte count.
- A new Append writes into `buf[(tail % cap) ...]` with byte-wise
  wraparound.
- Drain advances `head` (and `base` accordingly when `head` lapses
  through `cap`) — the cleared region is then available for the next
  append.

When `cap == 0`, the buffer is **disabled**: `Append` falls straight
through to `state.writeAt` like today. This is the safe default for
tests and the migration path; the GUC default of 16 MiB activates
the buffer in production.

## Append path

```
state.append(payload):
    record = encodeRecord(payload)
    if walBuffer == nil OR len(record) > walBuffer.cap:
        // Big record: bypass buffer entirely (don't try to fragment).
        writeAt(writePos, record)
        memRing.Append(writePos, record)  // walsender ring
        advance writeLSN
        return

    if walBuffer.resident + len(record) > walBuffer.cap:
        // Overflow drain: write enough buffered bytes to disk
        // to make room. Drain is contiguous-from-head so segment
        // ordering is preserved.
        drainBufferToSegments(needed = walBuffer.resident + len(record) - walBuffer.cap)

    walBuffer.append(record)            // bytes now in RAM only
    memRing.Append(writePos, record)    // walsender still sees them
    advance writeLSN
```

Crucially: `writeLSN` advances on every Append regardless of whether
the bytes are in the buffer or already on disk. The "writeLSN" name
keeps its existing contract — *bytes generated*, not *bytes
durable*. `flushedLSN` continues to track *bytes durable on disk*.

A new pointer `drainedLSN` tracks *bytes written into segment files*
(buffered bytes ≤ `drainedLSN` are NOT in `walBuffer`). Invariant:

```
flushedLSN ≤ drainedLSN ≤ writeLSN
walBuffer covers [drainedLSN+1, writeLSN]
walBuffer.resident == writeLSN - drainedLSN
```

`drainBufferToSegments(n)` writes `n` bytes from the buffer head to
their target segment file(s) via the existing `state.writeAt`
codepath. It then advances `head` and `drainedLSN`. The drained
segments are added to `s.dirty` so the next `flushUpTo` will
`dataSync` them — this is the "sync debt" the milestone calls out.

## FlushUpTo two-stage semantics

```
state.flushUpTo(lsn):
    // Stage 1: drain RAM → disk for any bytes in the buffer
    // up to lsn. After this, every byte ≤ lsn is in segment
    // files (possibly not yet fdatasync'd).
    if lsn > drainedLSN:
        drainBufferUpTo(lsn)

    // Stage 2 (unchanged): fdatasync every dirty segment up
    // through targetSeg(lsn). Existing 0007-0002 contract.
    syncDirtySegmentsUpTo(lsn)

    flushedLSN = lsn
```

`ErrLSNNotWritten` (the "lsn > writeLSN" case) is returned before
Stage 1 — same contract as today.

## On-segment format invariants

Unchanged. Bytes are appended to segment files in strictly increasing
LSN order. Drains write contiguous chunks; if a drain crosses a
segment boundary the existing `writeAt` segment-rotation logic
handles it.

## Walsender ring composition

The M0010-0002 `MemRing` continues to mirror **every** Append (both
buffered and direct-write paths) — walsender clients keep streaming
from RAM regardless of whether the bytes are in the WAL buffer or
already on disk. The two rings serve different consumers: walsender
reads (MemRing) vs. WAL writer overflow (this slice's walBuffer);
they don't share storage.

## Concurrency

The writer goroutine is the single serialization point — every
`Append`, `FlushUpTo`, and overflow-drain runs on it sequentially.
No new locks are introduced. Shared-buffer eviction (Pool) calls
`Writer.FlushUpTo` from caller goroutines; that already enqueues an
`opFlush` op which the writer goroutine drains.

## Failure handling

- Failed `state.writeAt` during overflow drain: `head` does NOT
  advance, the bytes stay in `walBuffer`, the next Append/Flush will
  retry. The error propagates to the caller of `Append`.
- Failed `dataSync` in Stage 2: `flushedLSN` does NOT advance; sync
  debt is preserved. Existing M0007-0002 behaviour.

## GUC

```
wal_buffers (int, ContextPostmaster, BootVal=16777216 = 16 MiB,
             range [0, 1 GiB])
```

`0` disables the buffer entirely (legacy behaviour). The setting is
plumbed `cmd/goopg start` → `OpenOptions.WALBuffers` →
`wal.Config.WALBuffers` → `state.walBuffer`.

## Rollout

- Default 16 MiB activates the buffer in every fresh deployment.
- Empty payloads are still rejected (existing `ErrEmptyPayload` for
  the EOS sentinel collision from M0007-0001).
- Recovery sees only segment files — buffer state is purely
  in-memory and disappears on crash. Crash-recovered records that
  were in the buffer at crash time are simply lost; that matches
  upstream where uncommitted WAL is also lost. Committed WAL is
  always durable because `Commit → FlushUpTo` runs Stage 1 + Stage 2
  before returning.

## Out of scope

- Eviction-driven drain orchestration from `storage.Pool` — the
  eviction path already calls `Writer.FlushUpTo(pageLSN)` which now
  has two-stage semantics, so the WAL-before-data invariant is
  automatically preserved. M0013-0002 verifies this with explicit
  tests and adds a `pg_stat_buffers` / dirty-eviction observability
  hook.
- Counters and startup log line for buffer activation —
  M0013-0003.
- Background drain (mimicking `wal_writer_delay`) — milestone says
  out of scope.

## Tests (this slice)

- `TestWALBufferDisabledByDefault` — `wal_buffers=0` → existing
  byte-for-byte behaviour. Append calls `writeAt` directly.
- `TestWALBufferRetainsRecordsInRAM` — small Appends stay in the
  buffer; segment file size on disk does NOT grow.
- `TestWALBufferOverflowDrainsToSegments` — Appending past the
  buffer cap drains in LSN order; segment file grows by exactly the
  drained byte count.
- `TestFlushUpToDrainsBufferThenSyncs` — `FlushUpTo(lsn)` flushes
  buffered bytes to disk AND fdatasyncs the touched segments.
- `TestWALBufferRecordLargerThanBufferBypasses` — a record larger
  than `wal_buffers` bypasses the buffer (writes straight to disk)
  rather than failing.
- `TestWALBufferReadAllRoundTrip` — encoded bytes round-trip
  through the buffer + drain + ReadAll, identical to the
  buffer-disabled control.
