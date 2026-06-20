# 0107-0007aj — WAL buffer reservation must cover the segment-crossing pad footprint

**Milestone**: M0107-0007 Phase D4 — WAL insert striping (correctness follow-up; surfaced by M0118-0001)
**Status**: Landed 2026-06-20

---

## Summary

The buffered stripe append paths reserved only `conservativeSize`
(`paddedLen + 64`) ring bytes per record, but a record whose LSN reservation
straddles a WAL segment boundary occupies up to **`2*conservativeSize`** ring
bytes: `reserveEmittedAndPublish` first emits an `XLOG_NOOP` pad over the gap
`[curr, boundary)` (via `emitSegmentPad → walBuffer.writeReserved`) and then
re-lands the record at the boundary (a second `writeReserved`). When the ring
was near-full at the crossing, the second `writeReserved` fell outside the
`[head, head+cap)` window and returned `errWALBufferReservedOutOfRange`.

The fix reserves the worst-case `2*conservativeSize` footprint in both
buffered append paths (`state.tryAppend` and `state.appendPGCompat`), so the
pad **and** the re-landed record always land inside the ring window.

---

## Problem

`reserveEmittedAndPublish` (`internal/wal/reserve_emitted.go`) keeps WAL
records from straddling a segment boundary: when
`startCandidate + total > boundary` it pads the gap and re-lands the record at
`boundary`. The pad is written through the ring by `emitSegmentPad`:

```go
// emitSegmentPad (onCrossSegment hook)
walBuf.writeReserved(int64(gapStart), pad)   // [gapStart, boundary)
// ...then the record is written at the boundary:
walBuf.writeReserved(int64(boundary), record) // [boundary, boundary+total)
```

So one append that crosses a boundary performs **two** `writeReserved` calls
whose combined footprint is `gapLen + total`. The crossing predicate
(`startCandidate + total > boundary`) guarantees `gapLen < total`, and the
re-landed record's emitted size is `≤ conservativeSize`, so the footprint is
strictly less than `2*conservativeSize`.

But both buffered append paths reserved only `conservativeSize`:

- `state.tryAppend` (fast path): `tryReserve(conservativeSize)`.
- `state.appendPGCompat` Path B (slow path): drains to `free() ≥ conservativeSize`.

`walBuffer.writeReserved` rejects any write whose range leaves
`[head, head+cap)`:

```go
if lsn < head || lsn+n > head+b.cap {
    return errWALBufferReservedOutOfRange
}
```

When the buffer was near-full, the record's write at `boundary` (the second
`writeReserved`) exceeded `head+cap` by up to `gapLen - 64` bytes — surfacing
as a query error from the slow path, or, worse, a **silently-swallowed** error
in the fast path (`tryAppend` returns `ok=false`, the caller retries via the
state loop over the already-advanced LSN cursor, leaving an unwritten **hole**
in the WAL stream).

### Why it was intermittent

A single writer cannot trip it: when `tryReserve` fails the buffer is full, so
`appendPGCompat` always takes the all-draining + buffer-resetting Path A, never
the boundary-crossing Path B. The overflow requires the buffer to be filled
*above* the single-writer drain threshold, which only happens under
concurrency (peer stripe reservations counted in `reservedBytes`, or Path B
running in the state loop alongside `tryAppend` callers). This is why the
`multiple-row-versions` isolation spec's 1,000,000-row single-transaction bulk
INSERT tripped it ~50% of runs (concurrent WAL machinery during the load).

---

## Solution

Reserve the worst-case footprint, `reserveSize = 2 * conservativeSize`, in both
buffered paths:

- `state.tryAppend` (PG-compat fast path): `canHold(reserveSize)`,
  `tryReserve(reserveSize)`, and the matching `releaseReservation(reserveSize)`.
- `state.appendPGCompat`: `needsDrain` / `canHold` / Path-B `need` all computed
  against `reserveSize`.

### Why `2*conservativeSize` is a strict, race-free upper bound

`footprint = gapLen + total_at_boundary`. The crossing predicate gives
`gapLen < total_at_startCandidate ≤ conservativeSize`, and
`total_at_boundary ≤ conservativeSize` (the `+64` margin covers the 40-byte
long page header at a segment boundary plus a 24-byte contrecord header). A
record can cross at most one boundary (`reserveEmittedAndPublish` panics if
`total > segSize`). Therefore `footprint < 2*conservativeSize` **always**,
independent of where the concurrently-advancing LSN cursor actually lands — so
the reservation does not need to read `curr` and has no stale-snapshot race.

### Concurrency correctness

The buffer invariant `resident() + reservedBytes ≤ cap` is maintained
atomically by `tryReserve`'s CAS loop. Given every in-flight reservation now
covers its own footprint, for each writer:

```
start + footprint ≤ tail + Σ(in-flight footprints) ≤ tail + reservedBytes
                 ≤ head + cap
```

`appendPGCompat` Path B is reached only when
`reserveSize ≤ free() − reservedBytes`, i.e. `tail + reservedBytes ≤
head + cap − reserveSize`; since its own `footprint ≤ reserveSize` and
`curr ≤ tail + reservedBytes`, its write also stays within `head + cap`.

### Cost

`conservativeSize` is record-sized (≈ payload + ~90 bytes); the WAL buffer is
multiple MiB (default and tests use 4 MiB). Reserving an extra
`conservativeSize` per in-flight record lowers the effective drain threshold by
a few hundred bytes — negligible — while eliminating the overflow entirely.

---

## Tests

`internal/wal/segment_cross_reservation_test.go`:

- `TestConcurrentAppendAcrossSegmentBoundariesNoOverflow` — 8 concurrent
  writers × 6000 appends of size-varying payloads through a small (12 000-byte)
  near-full ring with 32 KiB segments (hundreds of crossings). Asserts no
  `Append` returns `errWALBufferReservedOutOfRange`. Reverting
  `reserveSize` to `conservativeSize` reliably fails this test (verified 3/3
  runs); with the fix it is green under `-race -count=5`.

Full `go test -race ./internal/wal/` passes.

---

## Downstream effect

Unblocks promotion of the `multiple-row-versions` isolation spec to
`pass` (the SSI behaviour was already correct per [[0118-0001]] §9; the WAL
ring race was the only remaining blocker). See [[0118-0001]] §10.
