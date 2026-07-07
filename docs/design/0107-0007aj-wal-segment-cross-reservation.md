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

---

## 2026-07-07 update — Path B's re-check diverged from the gate (AI-20260707-000712-001)

The "Concurrency correctness" proof above assumed `appendPGCompat`'s Path B
body enforces the *same* inequality as the gate that routes into it
(`reserveSize ≤ free() − reservedBytes`). In the actual code this was false:
the gate (`needsDrain`, deciding Path A vs Path B) correctly subtracted
`reservedBytes.Load()`, but Path B's own pre-`AppendXLogPayload` headroom
check (previously `need := reserveSize - walBuf.free()`) did **not** — it
only ever looked at `free()`, which ignores bytes a concurrent
`tryAppend` fast-path caller has already claimed via `tryReserve` but not
yet published (so `resident()`/`tail` haven't advanced to cover them yet).

Both `tryAppend` (RLock) and the state-loop's `appendPGCompat` Path B run
lock-free with respect to each other (`appendMu` is only taken by Path A);
`tryAppend`'s `reservedBytes` claim is exactly the mechanism meant to make
concurrent reservations visible to any other headroom check — Path B's
recheck simply didn't consult it. Under enough scheduling contention (the
nightly race-gate run failed at `GOMAXPROCS`≈host-core-count under CI
load; a local repro needs `GOMAXPROCS=2` to widen the window reliably,
~1/15 tries at `-count=1`, every try at `-count=15`) a `tryAppend` claim
can grow `reservedBytes` between Path B's stale-free() check and its
`AppendXLogPayload` call, so the combined footprint exceeds `cap` and
`writeReserved` returns `errWALBufferReservedOutOfRange` — the exact
symptom `TestConcurrentAppendAcrossSegmentBoundariesNoOverflow` guards,
reached through a different concurrency path than the one this doc
originally analyzed (fast-path vs. slow-path racing, not fast-path vs.
fast-path).

**Fix**: Path B now claims its own `reserveSize` bytes via the same
`walBuf.tryReserve` / `releaseReservation` CAS pair `tryAppend` uses,
instead of a plain `free()` comparison — draining (subtracting
`reservedBytes`, matching the gate's formula) and retrying `tryReserve` in
a loop until the claim succeeds, then releasing it after
`PublishUpTo`/on error, exactly mirroring `tryAppend`'s protocol
(`internal/wal/writer.go`, `state.appendPGCompat`). This makes the two
concurrent callers of `AppendXLogPayload` (fast-path `tryAppend`, slow-path
`appendPGCompat`) agree on one atomic accounting mechanism instead of two
inconsistent formulas, closing the gap the "Concurrency correctness"
proof above missed.

Verified non-vacuous: reverting the fix reproduces the failure reliably
within `-count=15` at `GOMAXPROCS=2`/`-cpu=2`; with the fix, 15/15 (and a
separate 8/8 at `-count=1`) pass under the same conditions. Full
`go test -race ./internal/wal/` and the `scripts/ralph-precommit-test.sh`
pgbench smoke also pass.
