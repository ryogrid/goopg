# 0107-0007u — Slice B foundation 14: `stripeAppend` writer-side composer

Status: landed 2026-05-21 (dead code; consumed by the slice B call-site rewrite)

Milestone: [M0107-0007] (`docs/milestones/0107-perf-optimize.md`,
`docs/design/perf-optimize/07-wal-fsm-insert.md` §2 — 8-stripe WAL
insert locks).

Slice B foundation 14 of N. Composes the seven writer-side primitives
landed in [[0107-0007i]] / [[0107-0007l]] / [[0107-0007m]] /
[[0107-0007o]] / [[0107-0007p]] (with the cross-segment branch deferring
into [[0107-0007s]] `emitSegmentPad`) into one function that performs a
single stripe-locked append in the exact order the slice B writer
contract requires.

## Problem

Until this foundation lands, the slice B writer side is a six-call
chain that the call-site rewrite (the planned `state.append` rewrite
that retires `appendMu`) would have to assemble at every entry point:

```
unlock := locks.lockByProcNum(procNum)
defer unlock()
defer insertTracker.setInsertingAt(stripeForProcNum(procNum), lsnIdle)
start, prev := posTracker.reserveAndPublish(uint64(len(record)),
                                            stripeForProcNum(procNum),
                                            insertTracker)
if walBuf != nil { walBuf.writeReserved(int64(start), record) }
if memRing != nil { memRing.WriteReserved(int64(start), record) }
```

Six calls × multiple call sites (`state.append`, `state.appendRaw`,
group-commit fast path, walreceiver replay path) is six chances to
drift — wrong stripe number, missing END marker, wrong nil-guard order,
or a buried `return` that bypasses the deferred END publication and
leaves the stripe slot pinned at the failed reservation's start LSN
(which would freeze drain publication forever).

Foundation 14 lifts the composition into one place so the call-site
rewrite installs the chain as a single call, exactly matching the
shape of [[0107-0007t]] `publishVisibility` on the drain side.

## Symmetry with foundation 13

| | Foundation 13 (`publishVisibility`) | Foundation 14 (`stripeAppend`) |
|---|---|---|
| Side | Drain | Writer (per-stripe) |
| Locks | None | One stripe mutex (one of 8) |
| Writes bytes? | No | Yes — into walBuf and memRing |
| Advances visibility? | Yes — `publishTail` + `PublishUpTo` | No — drain owns that step |
| Caller | Drain goroutine, per tick | Stripe writer, per WAL record |
| PG counterpart | `WaitXLogInsertionsToFinish` + `LogwrtResult.Write` advance | `WALInsertLockAcquire` + `ReserveXLogInsertLocation` + `CopyXLogRecordToWAL` + `WALInsertLockRelease` |

The two composers form the complete slice B lifecycle: stripeAppend
lands bytes without advancing visibility; publishVisibility advances
visibility without writing bytes.

## Design

```go
func stripeAppend(
    locks *appendLockSet,
    posTracker *insertPosTracker,
    insertTracker *insertionTracker,
    walBuf *walBuffer,
    memRing *MemRing,
    procNum int32,
    record []byte,
) (start, prev uint64, err error)
```

Implementation:

```go
stripe := stripeForProcNum(procNum)
locks.locks[stripe].mu.Lock()
defer locks.locks[stripe].mu.Unlock()
defer insertTracker.setInsertingAt(stripe, lsnIdle)

start, prev = posTracker.reserveAndPublish(uint64(len(record)),
                                           stripe, insertTracker)
if walBuf != nil {
    if err := walBuf.writeReserved(int64(start), record); err != nil {
        return start, prev, err
    }
}
if memRing != nil {
    if err := memRing.WriteReserved(int64(start), record); err != nil {
        return start, prev, err
    }
}
return start, prev, nil
```

## Lock ordering

Matches the writer-side chain documented at the end of [[0107-0007t]]:

```
stripeAppend(locks, posTracker, insertTracker, walBuf, memRing, procNum, record)
  → appendLockSet.lockByProcNum            (one of 8 stripes acquired)
    → insertPosTracker.reserveAndPublish   (posMu held: reserveLocked +
                                            insertionTracker.setInsertingAt(start);
                                            posMu released)
        → (rare, only on segment crossings, fired synchronously under
           posMu) insertPosTracker.onCrossSegment → e.g.
           [[0107-0007s]] emitSegmentPad → buildSegmentPadRecord +
           walBuffer.writeReserved + MemRing.WriteReserved
    → walBuffer.writeReserved              (no lock; leaf)
    → MemRing.WriteReserved                (memRing.mu read-lock)
    → insertionTracker.setInsertingAt(stripe, lsnIdle)
  → drop stripe lock
```

Deferred unlock and deferred END marker exploit Go's LIFO defer ordering
so the END marker runs *before* the unlock — matches the tier above
exactly.

## Error handling and END marker

The deferred `setInsertingAt(stripe, lsnIdle)` runs whether or not the
inner write succeeded. This is load-bearing: if a write fails (e.g.
`errWALBufferReservedOutOfRange` from a caller-side range bug), leaving
the stripe slot stuck at the failed reservation's start LSN would block
the drain-side `tailPublisher` indefinitely — `lowestActiveLSN` would
permanently cap `safeTail` at the failed reservation's start, freezing
publication for the lifetime of the process.

The composer cannot un-reserve LSNs on a write failure because peer
stripes may have already reserved past the failed range. The error is
returned verbatim from the failing primitive so callers can pattern-
match (`errors.Is(err, errWALBufferReservedOutOfRange)`); diagnostic
attribution lives with the failing call.

`start, prev` are returned even on error so callers can log the
reservation that was granted (helpful for forensic analysis of a
ring-window contract violation).

## Nil-safety contract

| Argument | Nil → behaviour |
|---|---|
| `locks` | Reject with `errStripeAppendNilLocks` before any side effect |
| `posTracker` | Reject with `errStripeAppendNilPosTracker` before any side effect |
| `insertTracker` | Reject with `errStripeAppendNilInsertTracker` (nil would re-open pre-reserve race) |
| `walBuf` | Skip `writeReserved` (matches `emitSegmentPad`'s convention; supports `Config.WALBuffers == 0`) |
| `memRing` | Skip `WriteReserved` (matches; supports `wal_sender_memory_buffer == 0`) |

The three required-pointer rejections are structured errors rather than
panics so the call-site rewrite can fail-fast in production without
crashing the server on a wiring bug.

The empty-record case is also rejected with `errStripeAppendEmptyRecord`
rather than silently no-oping: (a) `reserveAndPublish(0, …)` panics on
size == 0 by design — we want a structured error from the composer, not
a panic for a benign empty call; (b) the slice B caller has no useful
semantics for "empty WAL insert"; (c) early rejection avoids acquiring
the stripe lock for a no-op call.

## Concurrency

Two `stripeAppend` calls with procNums that hash to different stripes
proceed fully in parallel — only the per-stripe mutex serialises within
a stripe. PG's `postgres/src/backend/access/transam/xlog.c` is the same
model: each backend takes `WALInsertLockAcquire(MyProcNumber %
NUM_XLOGINSERT_LOCKS)`, then `ReserveXLogInsertLocation`, then
`CopyXLogRecordToWAL`, then `WALInsertLockRelease`. goopg fuses the
equivalent five steps into this one composer so the call-site rewrite
stays a one-liner.

Cross-stripe correctness is provided by the foundations stripeAppend
composes:

- LSN disjointness: `insertPosTracker.reserveAndPublish` allocates
  disjoint LSN ranges under `posMu` ([[0107-0007p]]).
- Bytes-side concurrency: `walBuffer.writeReserved` is data-race-free
  for disjoint LSN ranges ([[0107-0007l]]).
- Memring-side concurrency: `MemRing.WriteReserved` takes the read
  lock and is data-race-free for disjoint LSN ranges ([[0107-0007o]]).
- Drain coordination: the END marker's atomic Store + `tailPublisher`'s
  CAS-loop publication ([[0107-0007n]] / [[0107-0007m]]) keeps drain
  readers race-free against stripe writers — once an END marker is
  observed, the bytes are guaranteed sequenced-before by Go's memory
  model (atomic Store happens-after the preceding `writeReserved` /
  `WriteReserved` calls in program order on the stripe writer).

Same-stripe correctness: the per-stripe `sync.Mutex.Lock()` serialises
both the LSN reservation and the byte writes, so the on-stripe
reservation order matches the on-stripe byte-write order — the
`xl_prev` chain remains well-formed within a stripe (the stripe's
records form an ordered prefix of the global LSN chain).

## Tests

Twelve regression tests in `internal/wal/stripe_append_test.go`,
following the foundation-style pattern (contract-anchored unit tests +
concurrent scenario + watchdog):

1. `TestStripeAppendHappyPathWritesBothRings` — Single insert, both
   rings get the bytes, END marker fires, publication makes them
   visible.
2. `TestStripeAppendNilLocksReturnsError` — Defensive nil-guard.
3. `TestStripeAppendNilPosTrackerReturnsError` — Defensive nil-guard.
4. `TestStripeAppendNilInsertTrackerReturnsError` — Defensive
   nil-guard (without it the pre-reserve race re-opens).
5. `TestStripeAppendEmptyRecordReturnsError` — Empty record rejected
   before any side effect; pos tracker / insertion tracker untouched.
6. `TestStripeAppendNilWalBufStillWritesMemRing` — `Config.WALBuffers
   == 0` path.
7. `TestStripeAppendNilMemRingStillWritesWalBuf` —
   `wal_sender_memory_buffer == 0` path.
8. `TestStripeAppendWalBufOutOfWindowReturnsErrorAndClearsStripe` —
   When the bytes-side write rejects (ring window violation), the END
   marker still fires so drain publication is not frozen.
9. `TestStripeAppendMemRingOutOfWindowReturnsErrorAndClearsStripe` —
   Same contract on the memring side (walBuf nil so memring error is
   reached directly).
10. `TestStripeAppendSelectsStripeByProcNum` — procNum & 0x7 stripe
    selection; per-stripe slot back to idle after every call.
11. `TestStripeAppendCrossSegmentEmitsPadAndChainsPrev` — segSize=200,
    three 80-byte records: third reservation crosses boundary 200,
    `onCrossSegment` fires `emitSegmentPad`, pad lands at [160, 200)
    with xl_prev=80, reservation lands at 200 with prev=160.
12. `TestStripeAppendConcurrentDisjointStripesProgressInParallel` —
    8 procNums × 200 records × 16 bytes (= 25 600 bytes total); race-
    clean under `-race`; per-stripe payload byte landed at every
    per-stripe start LSN; final starts form a permutation of `{0, 16,
    32, …, 25584}`.
13. `TestStripeAppendConcurrentSameStripeSerialise` — procNum 3 and
    procNum 11 (both stripe 3) × 500 records each; final reservation
    count = 1000 × 16 = 16 000 bytes; race-clean.
14. `TestStripeAppendConcurrentDrainConsistency` — 16 producers × 200
    records + a drain-style goroutine continuously calling
    `publishVisibility` with `posTracker.load()` as upperBound; final
    publication brings safe tail to 51 200; race-clean.
15. `TestStripeAppendWatchdog` — 5-second watchdog around the drain-
    consistency test; surfaces deadlock regressions before the
    package-level timeout.

Verified: `go test -race -count=1 -run 'TestStripeAppend'
./internal/wal/` PASS (1.03 s); `go test -race -count=1
./internal/wal/` PASS (3.18 s).

## PG counterpart

`postgres/src/backend/access/transam/xlog.c`'s
`XLogInsertRecord(rdata, ...)` builds the record and calls in sequence:

1. `WALInsertLockAcquire()` (or `WALInsertLockAcquireExclusive` for a
   full-page-write barrier) — picks `WALInsertLocks[MyProcNumber %
   NUM_XLOGINSERT_LOCKS]`.
2. `ReserveXLogInsertLocation(size, &StartPos, &EndPos, &PrevPtr)` —
   under the insert lock, advances `XLogCtl->Insert.CurrBytePos` and
   `Insert->PrevBytePos`.
3. `CopyXLogRecordToWAL(...)` — writes bytes into the shared
   `XLogCtl->pages` buffer at `StartPos`.
4. `WALInsertLockRelease()` — drops the insert lock; under the hood,
   `WALInsertLockUpdateInsertingAt` is called inside step (2) so the
   per-lock `insertingAt` field is published while the lock is held.

goopg's `stripeAppend` is the direct moral equivalent:

| PG step | goopg step |
|---|---|
| `WALInsertLockAcquire` | `appendLockSet.lockByProcNum` (`stripeForProcNum(procNum)`) |
| `ReserveXLogInsertLocation` + `WALInsertLockUpdateInsertingAt` | `insertPosTracker.reserveAndPublish` (both updates sealed under `posMu`, mirroring PG's insert-lock-held update) |
| `CopyXLogRecordToWAL` (into `XLogCtl->pages`) | `walBuffer.writeReserved` |
| (PG has no separate memring; walsenders consume `XLogCtl->pages`) | `MemRing.WriteReserved` (M0010-0002 walsender RAM mirror) |
| (END publication: PG resets `insertingAt` to `InvalidXLogRecPtr` inside `WALInsertLockRelease`) | `insertionTracker.setInsertingAt(stripe, lsnIdle)` (deferred, runs before unlock) |
| `WALInsertLockRelease` | `appendLockSet` unlock (deferred) |

## Why does this primitive exist if it is only twelve lines?

Three reasons, matching the rationale that made [[0107-0007s]]
`emitSegmentPad` and [[0107-0007t]] `publishVisibility` worth lifting
out:

1. **Lock-ordering documentation.** The doc comment pins the exact
   writer-side composition the call-site rewrite must perform. Future
   drift (e.g., forgetting the END marker before unlock, or skipping
   the nil-guard before `walBuf.writeReserved`) would fail the
   foundation's contract-anchored tests rather than slip through a
   buried call site.
2. **Single error-handling site.** The deferred END marker plus the
   per-ring nil-guard plus the per-ring error propagation are easy to
   get wrong if duplicated at every call site. Lifting them into one
   composer means the call-site rewrite cannot accidentally bypass the
   END marker on an error path.
3. **Foundation-first pattern.** The slice B call-site rewrite already
   consumes thirteen foundations. Lifting the writer composition into
   a fourteenth lets the rewrite stay a structural mechanical change
   (move-the-call, not invent-the-call), and the composer's tests
   exercise the writer-side lifecycle in isolation so the rewrite can
   focus on the integration concerns (e.g., how `state.append`'s four
   invariants partition between per-stripe local state and shared
   state).

## Out of scope

- Mounting `appendLockSet` + `insertPosTracker` + `insertionTracker` +
  `walBuf` + `memRing` on `Writer` and switching `state.append` to
  call `stripeAppend`. Call-site rewrite scope; multi-loop work
  because `state.appendMu`'s four invariants (writePos / walBuf /
  memRing / writeLSN) must split into per-stripe local state vs.
  shared state.
- Drain coordination with concurrent stripe writes. Today
  `drainBufferBytes` runs under `appendMu`; the rewrite must let
  drain run concurrently with stripe writers by consuming
  `publishVisibility`'s return as the drain ceiling for
  `walBuffer.readForDrain` / `writeAt` / `walBuffer.advanceHead`.
- Deciding whether [[0107-0007h]] `lsnAllocator` becomes dead-code-
  removed once the call-site converges on `insertPosTracker` +
  `insertionTracker` + `tailPublisher` + `reserveAndPublish` +
  `publishTail` + `emitSegmentPad` + `publishVisibility` +
  `stripeAppend` — `reserve` remains in the API as a callable
  primitive for non-stripe callers.
- Group-commit interaction. The slice B writer chain produces records
  in the WAL buffer; the fdatasync amortisation in
  `writer.go:611+` (M0098-0002) remains unchanged because it operates
  on the cumulative buffer content, not per-record.
