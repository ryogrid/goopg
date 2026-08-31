# 0107-0007v — `stripeWriterCore` packaging struct for slice B

**Status**: accepted (2026-05-21)

**Milestone**: M0107-0007 (Phase D4 — WAL insert striping). Slice B
foundation 15 of N. Parent: `docs/design/perf-optimize/07-wal-fsm-insert.md` §2.

Foundations 1–14 (see [0107-0007h], [0107-0007i], [0107-0007j],
[0107-0007k], [0107-0007l], [0107-0007m], [0107-0007n], [0107-0007o],
[0107-0007p], [0107-0007q], [0107-0007r], [0107-0007s], [0107-0007t],
[0107-0007u]) landed the slice B primitives in isolation. This foundation
packages the seven writer-side primitives into one struct with a single
constructor that wires the cross-segment hook, so the eventual call-site
rewrite mounts ONE field on `Writer` (a `*stripeWriterCore`) instead of
seven and reduces every consumer site to a one-line method call.

## Problem

The call-site rewrite under
`docs/design/perf-optimize/07-wal-fsm-insert.md` §2 needs to:

1. Replace `state.appendMu` with `appendLockSet` plus per-stripe locking.
2. Replace single-thread `writePos` advance with `insertPosTracker.reserveAndPublish`.
3. Track per-stripe insert-in-progress LSNs in `insertionTracker`.
4. Publish a monotonically-advancing safe tail via `tailPublisher`.
5. Stamp pad records into cross-segment gaps via `emitSegmentPad`.

Without packaging, the rewrite would need to:

- Add five new fields to either `Writer` or `state` (each a `*Foo`).
- Re-encode the constructor wiring (`onCrossSegment` closure capturing
  the rings) at every site that constructs a `Writer`.
- Re-encode the lock-ordering tier at every site that invokes the
  full chain.

That is the kind of mechanical duplication that the foundation-first
pattern exists to prevent.

## Solution

`internal/wal/stripe_writer_core.go` defines `stripeWriterCore` — a
six-field struct that owns the four "new" primitives
(`appendLockSet`, `insertPosTracker`, `insertionTracker`,
`tailPublisher`) and borrows the two ring buffers (`walBuffer`,
`MemRing`) by pointer from the lifetime owner (`Writer`). The
constructor:

```go
func newStripeWriterCore(segSize, startCurr, startPrev uint64, walBuf *walBuffer, memRing *MemRing) *stripeWriterCore
```

wires `insertPosTracker.onCrossSegment` to a closure that captures
`walBuf` / `memRing` and invokes `emitSegmentPad`. The closure panics
on emit failure — `onCrossSegment` has no error return, and
[0107-0007s] requires fatal escalation on a failed pad emit (a
silently dropped pad would corrupt the xl_prev chain across the
boundary, and rolling back the reservation would race peer stripes
that have already observed the new `curr`).

Method surface:

| Method | Delegates to | Purpose |
|---|---|---|
| `Append(procNum, record)` | `stripeAppend` | One stripe-locked write |
| `PublishUpTo(upperBound)` | `publishVisibility` | One drain-side tail advance |
| `Load()` | `insertPosTracker.load` | Read (curr, prev) snapshot for upperBound |
| `PublishedTail()` | `tailPublisher.load` | Read raw watermark for diagnostics |

Each method is nil-safe on the receiver — methods on `nil` return
structured errors / zeros so transitional call-site states or
fixtures with the core unset are benign.

## Borrowed vs owned

The four owned primitives are exclusive to the slice B insert path.
The two ring buffers (`walBuf` / `MemRing`) are also referenced by:

- `state.append` (legacy path; remains during the rewrite for fallback
  semantics not yet covered by slice B).
- `state.drainBufferBytes` (the writeAt + advanceHead drain).
- Walsender's `RecordIterator` (reads from `MemRing`).

Storing them as borrowed pointers in the core matches the lifetime
reality: `Writer` constructs the rings once and shares them with
both the legacy path and the new core. The rewrite does NOT
duplicate the rings under the core; doing so would either fork the
on-disk visibility (legacy path sees different bytes than slice B)
or require expensive cross-ring synchronisation.

## Cross-segment hook policy

`insertPosTracker.onCrossSegment` signature is
`func(start, boundary, prev uint64)` — no error return. The closure
the constructor installs:

```go
onCross := func(start, boundary, gapPrev uint64) {
    if err := emitSegmentPad(walBuf, memRing, start, boundary, gapPrev); err != nil {
        panic(fmt.Sprintf("wal: stripeWriterCore: cross-segment pad emit failed: %v", err))
    }
}
```

`emitSegmentPad` can fail under three conditions:

1. `boundary <= gapStart` — defence in depth; `insertPosTracker.reserveLocked`
   never produces such a gap, but a future caller might.
2. Malformed `padLen` from `buildSegmentPadRecord` (padLen < 24, padLen == 25).
3. Range violation on `walBuffer.writeReserved` or `MemRing.WriteReserved`.

Cases 1 and 3 are wiring bugs. Case 2 is a real corner: gaps
of 1..23 bytes and gaps of exactly 25 bytes cannot encode an
XLOG_NOOP record. The call-site rewrite will round record sizes to
8-byte MAXALIGN so reservation gaps are multiples of 8; the
smallest multiple ≥ 24 is 24, so multiples of 8 in [24, ∞) are all
valid. The 25-byte case is reachable only from caller-side
alignment violations.

This packaging foundation does not invent the alignment policy. It
encodes the "fatal escalation" half of the contract via panic;
preventing the panic by enforcing alignment is the call-site
rewrite's responsibility.

## Lock-ordering tier

Combining [0107-0007u] `stripeAppend`'s writer chain with
[0107-0007t] `publishVisibility`'s drain chain:

```
(stripe writer, per-record):
  core.Append(procNum, record)
    → appendLockSet.lockByProcNum               (one of 8 stripes)
      → insertPosTracker.reserveAndPublish      (posMu held)
          → (rare) onCrossSegment(start, boundary, gapPrev)
              → emitSegmentPad → buildSegmentPadRecord +
                walBuffer.writeReserved + MemRing.WriteReserved
          → insertionTracker.setInsertingAt(stripe, start)
        (posMu released)
      → walBuffer.writeReserved
      → MemRing.WriteReserved
      → insertionTracker.setInsertingAt(stripe, lsnIdle)
    → drop stripe lock

(drain goroutine, per-tick):
  core.PublishUpTo(upperBound)
    → tailPublisher.publishUpTo(upperBound, insertionTracker)
    → walBuffer.publishTail(safeTail)
    → MemRing.PublishUpTo(safeTail)
```

The two chains are tier-disjoint by design: stripe writers and the
drain goroutine never block each other except where the
publication watermark requires a stripe slot to be observed idle.

## Tests

Ten regression tests in `internal/wal/stripe_writer_core_test.go`:

- `TestStripeWriterCoreAppendHappyPath` — single Append + PublishUpTo;
  pre-publish reads miss, post-publish reads hit byte-identical bytes
  in both rings; Load reflects post-reservation position.
- `TestStripeWriterCoreNilReceiverGuards` — methods on `nil` return
  structured errors / zeros; transitional states are benign.
- `TestStripeWriterCoreNilRingsStillProgress` — three sub-cases
  (walBuf nil, memRing nil, both nil); each Config combination
  routes correctly.
- `TestStripeWriterCoreRejectsZeroSegSize` — constructor invariant
  enforced via `newInsertPosTracker`'s panic.
- `TestStripeWriterCoreRecoveryResume` — non-zero `startCurr` /
  `startPrev` propagate; first append lands at startCurr with
  prev=startPrev.
- `TestStripeWriterCoreCrossSegmentEmitsPad` — segSize=200 + three
  80-byte reservations; third crosses boundary; pad lands in both
  rings; triggering reservation has prev=160.
- `TestStripeWriterCorePublishUpToCapsAtActiveStripe` — direct slot
  manipulation pins the drain-side cap behaviour.
- `TestStripeWriterCorePublishedTailReflectsInternalState` — raw
  watermark accessor; monotonic.
- `TestStripeWriterCoreConcurrentAppendsAndPublish` — 8 writers ×
  200 records + a continuous drain goroutine; race-clean under
  `-race`; final watermark matches expected total.
- `TestStripeWriterCoreConcurrentCompletesUnderWatchdog` — 5-second
  watchdog around the concurrent scenario.
- `TestStripeWriterCoreAppendEmptyRecord` — empty record rejection
  pass-through; tracker state untouched.

Verified: `go test -race -count=1 -run 'TestStripeWriterCore'
./internal/wal/` PASS (1.02 s); `go test -race -count=1
./internal/wal/` PASS (3.17 s).

## Why this is dead code

No production call site invokes `newStripeWriterCore` yet — the
struct is one constructor invocation away from being mounted on
`Writer`, but doing so requires the call-site rewrite that splits
`state.appendMu`'s four invariants (writePos / walBuf / memRing /
writeLSN) into per-stripe local state vs. shared state. That work
is multi-loop scope and explicitly deferred.

The foundation-first pattern (slice C [0107-0007b] / [0107-0007c]
/ [0107-0007d] before [0107-0007e] / [0107-0007f] / [0107-0007g]
consumed them) lets the call-site rewrite be a mechanical
structural change rather than an "invent + rewire" change. With
this packaging foundation in place, the rewrite's site footprint
is:

- `Writer` gains one field: `core *stripeWriterCore`.
- `NewWriter` adds one constructor call after `walBuf` / `memRing`
  are built.
- `state.append`'s body switches to `s.core.Append(procNum, encoded)`.
- The drain goroutine calls `s.core.PublishUpTo(...)` before
  `readForDrain` / `writeAt` / `advanceHead`.

Everything else (the four owned primitives, the seven composing
foundations, the cross-segment hook, the publication walker) is
already in place.

## Out of scope

- Mounting `*stripeWriterCore` on `Writer` — the call-site rewrite.
- Splitting `state.appendMu`'s four invariants — multi-loop scope.
- Drain goroutine restructuring — `drainBufferBytes` currently runs
  under `appendMu`; the rewrite must let drain run concurrently
  with stripe writers by consuming `core.PublishUpTo`'s return as
  the drain ceiling.
- 8-byte MAXALIGN of record sizes to guarantee valid pad gaps —
  the call-site rewrite's pre-Append step.
- Deciding whether [0107-0007h] `lsnAllocator` becomes
  dead-code-removed once the call-site converges on the
  insertPosTracker + insertionTracker + tailPublisher trio — that
  decision belongs to the rewrite's cleanup phase.

## References

- Parent design: `docs/design/perf-optimize/07-wal-fsm-insert.md` §2
  (WAL Insert striping).
- PG counterpart: `postgres/src/backend/access/transam/xlog.c` —
  `XLogInsertRecord` → `WALInsertLockAcquire` →
  `ReserveXLogInsertLocation` → `CopyXLogRecordToWAL` →
  `WALInsertLockRelease`. goopg's `stripeWriterCore.Append` fuses
  the equivalent five steps; `stripeWriterCore.PublishUpTo`
  mirrors `WaitXLogInsertionsToFinish` + `XLogCtl->LogwrtResult.Write`
  advance.
- Foundation-first precedent: slice C [0107-0007e]
  `selectFSMCandidatePage` packaged the slice C foundations before
  the call-site rewrite consumed them.
- Foundations consumed: [0107-0007h], [0107-0007i], [0107-0007j],
  [0107-0007k], [0107-0007l], [0107-0007m], [0107-0007n],
  [0107-0007o], [0107-0007p], [0107-0007q], [0107-0007r],
  [0107-0007s], [0107-0007t], [0107-0007u].
