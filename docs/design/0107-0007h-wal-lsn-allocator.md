# 0107-0007h — WAL `lsnAllocator` segment-aware atomic LSN reserve (Phase D4 slice B foundation 1)

Status: accepted (M0107-0007 partial — slice B foundation 1 of N)
Milestone: M0107-0007 (Phase D4 — WAL insert striping + FSM page distribution)
Parent chapter: [`docs/design/perf-optimize/07-wal-fsm-insert.md`](perf-optimize/07-wal-fsm-insert.md) §2

## Summary

Adds a self-contained `lsnAllocator` primitive to `internal/wal/lsn_alloc.go`:
an atomic LSN reservation surface backed by `atomic.Uint64` + a `rotateMu
sync.Mutex` for segment-boundary crossings. The common in-segment path is
a single CAS; segment crossings serialise on `rotateMu` so an optional
`onCrossSegment(start, boundary uint64)` hook can pad the segment tail
and/or open the next segment file exactly once per crossing.

The primitive is the LSN-allocation half of parent chapter §2's striped
`wal.Writer.appendLocks [8]paddedMutex` plan. It is not wired into
`Writer.Append` or `state.append` in this loop — the call-site rewrite
that swaps `state.appendMu` for the 8 stripe locks and replaces the
sequential `writePos += len(stream)` advance with `lsnAllocator.reserve`
is multi-loop scope (parent chapter notes the four `state.appendMu`
invariants — `writePos`, `walBuf` ring state, `memRing` append, and
`writeLSN` advance — must be split into per-stripe local vs. shared
state). The foundation-first pattern matches slice C ([[0107-0007b]] /
[[0107-0007c]] / [[0107-0007d]] all landed before [[0107-0007e]] /
[[0107-0007f]] / [[0107-0007g]] consumed them).

## API

```go
// internal/wal/lsn_alloc.go (new file)

type lsnAllocator struct {
    next     atomic.Uint64
    rotateMu sync.Mutex
    segSize  uint64
    onCrossSegment func(start, boundary uint64)
}

func newLSNAllocator(startLSN, segSize uint64,
    onCross func(start, boundary uint64)) *lsnAllocator

func (a *lsnAllocator) load() uint64
func (a *lsnAllocator) reserve(size uint64) uint64
```

`reserve` requires `0 < size <= segSize`. The contract is purely a
contiguous byte-range claim: the returned `start` and the implicit end
`start+size` are guaranteed disjoint across concurrent callers, and
later `reserve` calls never observe an LSN below `start+size`. Segment
crossings advance `next` past the boundary; the gap `[oldNext,
boundary)` is what the caller pads in `onCrossSegment`.

## Why a primitive instead of inlining

Three reasons:

1. **Self-contained CAS + rotateMu interplay is the hardest part of slice
   B.** Mixing it with the byte-stream encode (`emitWithPageHeaders`),
   `walBuf.append`, `memRing` mirror, and disk `writeAt` in one
   commit makes a regression harder to root-cause. Landing the
   primitive standalone lets the unit tests pin the LSN-reserve
   correctness independently before slice B's call-site rewrite drops
   `state.appendMu`.
2. **PG's `ReserveXLogInsertLocation` is also a free-standing helper.**
   The PG counterpart sits at `postgres/src/backend/access/transam/
   xlog.c` (search for `ReserveXLogInsertLocation` — line numbers
   drift between minors; the symbol is the anchor). It is called
   exactly once per WAL record append from inside
   `XLogInsertRecord`, and it owns the `XLogCtl->Insert.CurrBytePos`
   advance plus the segment-crossing path. The goopg primitive
   mirrors that structure.
3. **It is independently testable.** Reserving billions of LSNs
   inside the `Writer.Append` path costs disk I/O; running it against
   the standalone primitive lets the CAS path and rotateMu path be
   exercised under `-race` in milliseconds.

## Algorithm

```text
reserve(size):
  loop:
    old   := next.Load
    end   := old + size
    oldSeg := old / segSize
    endSeg := (end - 1) / segSize
    if oldSeg == endSeg:
      // Fast path — single CAS.
      if next.CAS(old, end): return old
      continue

    // Cross-segment slow path.
    rotateMu.Lock
    old   = next.Load            // recheck under rotateMu
    end   = old + size
    oldSeg = old / segSize
    endSeg = (end - 1) / segSize
    if oldSeg == endSeg:
      // Another writer rotated past us; drop back to fast path.
      rotateMu.Unlock; continue
    boundary := (oldSeg + 1) * segSize
    if onCrossSegment != nil: onCrossSegment(old, boundary)
    next.Store(boundary + size)
    rotateMu.Unlock
    return boundary
```

Key invariants:

- **No record straddles a segment boundary.** If a reservation's
  computed end lies in a different segment than its start, the
  reservation is placed at the start of the new segment and the
  gap `[old, boundary)` is left for the hook to pad with a NOOP
  WAL record (the PG counterpart is the `XLOG_NOOP` pad written
  by `AdvanceXLInsertBuffer` when crossing).
- **`onCrossSegment` fires exactly once per actual crossing.** Two
  goroutines that both observe a crossing reservation contend on
  `rotateMu`; whichever loses the race re-reads `next` after the
  winner advanced it and falls into the `oldSeg == endSeg` early-
  out (no double-pad).
- **Bounded `size`.** Records larger than `segSize` are out of
  scope for this primitive (PG WAL records are bounded; goopg's
  `state.append` already rejects records larger than the segment).
  Callers must split or pad oversized payloads themselves.

## Concurrency safety

`reserve`'s fast path is wait-free per goroutine (a goroutine that
fails its CAS must observe a strictly increasing `next` on the next
iteration, so progress is monotonic). The slow path takes `rotateMu`
exactly once per crossing observed; under steady inserts this is
~1/segSize of calls (default ≈ 1 every 16 MiB of records).

`rotateMu` does not order with the future `appendLocks [8]paddedMutex`
stripe locks; a stripe holder may take `rotateMu` while another
stripe holder is in its critical section. The lock order is always
`stripe.Lock → (optional) rotateMu.Lock → rotateMu.Unlock →
stripe.Unlock`, so the two layers do not deadlock as long as
`onCrossSegment` does not call back into any stripe lock — the
default expectation is that the hook only touches segment-file
state (open new segment, fsync-recycle previous, etc.), which is
disjoint from the stripe-protected `walBuf` / `memRing` /
`writePos` mutations.

`onCrossSegment` is run under `rotateMu`; multiple crossings cannot
overlap. Hook implementations may therefore mutate segment-file
metadata without further locking.

## Regression coverage

`internal/wal/lsn_alloc_test.go`:

- `TestLSNAllocatorReserveContiguousMonotonic` — three sequential
  reserves of 10/20/30 yield 0/10/30; `load()` advances to 60.
- `TestLSNAllocatorReserveStartLSN` — non-zero start (1000) with a
  large segment (4 KiB) confirms the offset arithmetic so recovery
  callers can resume at `detectWritePos`'s tail without surprises.
- `TestLSNAllocatorCrossSegmentInvokesHook` — fill segment 0 to byte
  1000 (no hook fires), then a 50-byte reserve crosses to segment 1
  → hook fires exactly once with `(start=1000, boundary=1024)`; the
  reservation lands at 1024.
- `TestLSNAllocatorReserveAtExactBoundaryNoHook` — when `next ==
  boundary` exactly, the reservation is fully in the new segment
  and the fast path runs with no hook (oldSeg == endSeg both equal
  the new segment's index).
- `TestLSNAllocatorReserveInvalidSizePanics` — size 0, size > segSize
  all panic; the contract is strict.
- `TestLSNAllocatorNewRejectsZeroSegSize` — `newLSNAllocator(_,0,_)`
  panics so misconfiguration fails fast.
- `TestLSNAllocatorConcurrentReservesDisjoint` — 32 goroutines × 100
  reserves of 16 bytes against a 1 MiB single segment yield a
  perfect permutation of `[0, 51200)` in 16-byte chunks; the
  rotation hook is wired to `t.Errorf` so any spurious crossing
  fails the test.
- `TestLSNAllocatorConcurrentCrossSegmentHookOncePerBoundary` — 16
  goroutines all race to reserve across the same boundary (start at
  byte 230 of a 256-byte segment, 40-byte records). After the race,
  the actual crossing-count matches `(lastSegment - firstSegment)`
  (one hook fire per real boundary), every reservation occupies a
  contiguous range inside a single segment, and the sorted starts
  are disjoint by ≥ `sz`.
- `TestLSNAllocatorReserveAcrossTwoBoundaries` — walks the
  allocator across two distinct segment boundaries; hook payloads
  are `[{80, 100}, {195, 200}]` — pre-pad next-LSN and post-pad
  boundary for each crossing.

Verified: `go test -race -count=1 -run 'TestLSNAllocator'
./internal/wal/` PASS (1.02 s); `go test -race -count=1
./internal/wal/` PASS (3.09 s).

## Out of scope

- **Wiring into `Writer.Append` / `state.append`.** The next slice B
  foundations split `state.appendMu`'s four invariants (writePos,
  walBuf, memRing, writeLSN) and add `appendLocks [8]paddedMutex` on
  the Writer. The LSN allocator is the third piece, not the first
  to land in production code.
- **`paddedMutex` in `internal/wal`.** Slice A introduced
  `paddedMutex` in `internal/executor`; slice B's stripe-array needs
  it too, but that introduction is a separate slice B foundation —
  the LSN allocator is one of several disjoint sub-primitives.
- **PG `XLogRecord.xl_prev` chain integrity.** The existing
  `state.append`'s `prevRecPtr` machinery is unaffected by this
  primitive; the LSN allocator only hands out byte ranges. The
  call-site rewrite that consumes the allocator will keep
  `prevRecPtr` advance under the stripe lock so records' xl_prev
  remains the LSN of the *previous record on the same stripe* — PG
  has the same per-stripe `xl_prev` model.
- **Segment file management.** `onCrossSegment` is the seam where
  the caller hooks file open / fsync-recycle / preallocate work;
  the allocator deliberately does not own segment files (that lives
  in `s.openSegment` / `s.preallocateSegment`).

## PG-compat

None for this slice. The primitive is in-memory; no on-disk file
format, WAL record, catalog tuple, or wire-protocol byte changes.
The byte-range it hands out is exactly the same `[start, start+size)`
that today's `s.append` materialises by `writePos += len(stream)` —
the primitive only changes *how* the next LSN advances (atomic CAS
vs. mutex-held increment), not *which* LSN a given record receives
under a given workload.
