# 0107-0007y — Slice B foundation 16: `stripeAppendBuild` encode-after-reserve composer

Status: landed 2026-05-21 (dead code; consumed by the slice B call-site
rewrite at the PG-compat write entry point)

Milestone: [M0107-0007] (`docs/milestones/0107-perf-optimize.md`,
`docs/design/perf-optimize/07-wal-fsm-insert.md` §2 — 8-stripe WAL
insert locks).

Slice B foundation 16 of N. Adds the encode-after-reserve sibling of
[[0107-0007u]] `stripeAppend` so the slice B call-site rewrite can
materialise PG-compat record bytes using the prev LSN that
[[0107-0007k]] `insertPosTracker.reserveAndPublish` returns.

## Problem

[[0107-0007u]] `stripeAppend` takes a pre-encoded `record []byte` and
writes it into both rings at the reserved start LSN. That contract is
sufficient for goopg-internal records (`encodeRecord`) which have no
prev-LSN linkage, but it is structurally insufficient for PG-compat
records (`encodeRecordXLog`): the `XLogRecord.Prev` (`xl_prev`) field
stamped into the record header carries the immediately-preceding
record's start LSN, which is the same value
`insertPosTracker.reserveAndPublish` produces as its second return —
known only AFTER the LSN reservation.

Two non-options:

1. **Pre-encode with a stale prev.** The call site reads
   `state.prevRecPtr` before the stripe lock, encodes with it, then
   calls stripeAppend. Under concurrent stripe writers, the prev seen
   pre-lock is not the prev assigned by reserveAndPublish (peers
   between read and lock have advanced the chain). Result: a permanent
   xl_prev mismatch — pg_waldump fails and a PG18 standby's
   `XLogValidateRecordHeader` fast-fails.

2. **Patch the encoded record in-place after reservation.** The header
   is at a fixed offset, but mutating the buffer post-encode forces
   the writer to hold an exclusive view of the record bytes across
   the reservation + write window — exactly the cross-stripe coupling
   slice B is trying to eliminate.

The clean answer is to invert the order: reserve first, then encode
with the assigned prev, then write.

## Symmetry with foundation 14

| | Foundation 14 (`stripeAppend`) | Foundation 16 (`stripeAppendBuild`) |
|---|---|---|
| Input | `record []byte` (already encoded) | `size int`, `build func(prev uint64) ([]byte, error)` |
| When does encoding happen? | Pre-lock (caller's responsibility) | Inside the stripe critical section, post-reservation |
| Prev-linkage support? | No — caller must encode with whatever prev they like | Yes — build receives the joint-atomic prev from reserveAndPublish |
| Use case | Legacy goopg-internal records, walreceiver replay (raw byte stream) | PG-compat records with `xl_prev` linkage |
| END marker | Fires regardless of write outcome | Fires regardless of build / write outcome |

The two functions share `appendLockSet`, `insertPosTracker`,
`insertionTracker`, `walBuffer`, `MemRing` — they are the same composer
with a builder hook spliced in between the reservation and the byte
writes.

## Design

```go
func stripeAppendBuild(
    locks *appendLockSet,
    posTracker *insertPosTracker,
    insertTracker *insertionTracker,
    walBuf *walBuffer,
    memRing *MemRing,
    procNum int32,
    size int,
    build func(prev uint64) ([]byte, error),
) (start, prev uint64, err error)
```

Body, mirroring `stripeAppend` with `build(prev)` slotted between the
reservation and the byte writes:

```go
stripe := stripeForProcNum(procNum)
locks.locks[stripe].mu.Lock()
defer locks.locks[stripe].mu.Unlock()
defer insertTracker.setInsertingAt(stripe, lsnIdle)

start, prev = posTracker.reserveAndPublish(uint64(size), stripe, insertTracker)

record, berr := build(prev)
if berr != nil { return start, prev, berr }
if len(record) != size { return start, prev, errStripeAppendBuildSizeMismatch }

if walBuf  != nil { walBuf.writeReserved(int64(start), record) }
if memRing != nil { memRing.WriteReserved(int64(start), record) }
```

`(*stripeWriterCore).AppendBuilt(procNum, size, build)` thinly wraps
this so the call-site rewrite mounts one method on `Writer.core` for
PG-compat records (alongside `Append` for raw bytes).

## Contracts

- **size > 0.** Zero-size reservations panic inside reserveAndPublish
  by design; rejecting them as a structured error at the composer
  boundary keeps the failure mode caller-visible.
- **build != nil.** Distinct sentinel; nil build is a wiring bug.
- **build returns exactly `size` bytes.** Mismatch corrupts peer
  stripes (over) or publishes zeros (under). Validated before any
  ring write.
- **build error → propagate verbatim.** Reservation cannot be
  unwound (peer stripes may have already advanced the curr); the END
  marker fires via defer so publication never freezes; the caller is
  expected to escalate the error fatally at the WAL append boundary.
- **Cross-segment crossings are transparent.** Same as stripeAppend:
  `posTracker.onCrossSegment` (wired to `emitSegmentPad` by
  `newStripeWriterCore`) fires synchronously inside reserveAndPublish
  under posMu, so build observes the prev that points at the pad
  record (not the pre-pad record).

## Lock-ordering tier

```
stripeAppendBuild(locks, posTracker, insertTracker, walBuf, memRing,
                  procNum, size, build)
  → appendLockSet.lockByProcNum            (one of 8 stripes)
    → insertPosTracker.reserveAndPublish   (posMu held)
        → (rare) onCrossSegment(start, boundary, gapPrev)
            → emitSegmentPad → buildSegmentPadRecord +
              walBuffer.writeReserved + MemRing.WriteReserved
    → build(prev)                          (caller's encoder; no lock)
    → walBuffer.writeReserved              (no lock; leaf)
    → MemRing.WriteReserved                (memRing.mu read-lock)
    → insertionTracker.setInsertingAt(stripe, lsnIdle)
  → drop stripe lock
```

`build` runs under the stripe lock (not under posMu — posMu is
released by reserveAndPublish before this point), so multi-stripe
encoders cannot serialise on the same posMu. The stripe lock keeps
the per-stripe (reserve, build, write) sequence atomic with respect
to peer reservations on the same stripe.

## Tests

`internal/wal/stripe_append_build_test.go` (11 tests):

- `TestStripeAppendBuildHappyPathReceivesPrev` — two-record sequence
  pins `build` receiving prev=0 for record #1 and prev=start1 for
  record #2; verifies the prev stamped into the encoded record body
  matches what the closure saw.
- `TestStripeAppendBuildNilLocksReturnsError`,
  `TestStripeAppendBuildNilPosTrackerReturnsError`,
  `TestStripeAppendBuildNilInsertTrackerReturnsError`,
  `TestStripeAppendBuildNilBuildReturnsError` — nil-guard surface.
- `TestStripeAppendBuildZeroSizeReturnsError` — size ∈ {0, -1}
  → `errStripeAppendEmptyRecord` (shared with stripeAppend).
- `TestStripeAppendBuildBuildErrorPropagatesAndClearsStripe` — build
  returning a sentinel error propagates the error AND leaves the
  insertion tracker idle (END marker fired).
- `TestStripeAppendBuildSizeMismatchReturnsError` — build returns
  15 or 17 bytes when 16 was reserved → `errStripeAppendBuildSizeMismatch`,
  END marker still fired.
- `TestStripeAppendBuildNilWalBufStillWritesMemRing`,
  `TestStripeAppendBuildNilMemRingStillWritesWalBuf` — independent
  nil-safety per ring.
- `TestStripeAppendBuildCrossSegmentChainsPrevAcrossPad` — 128-byte
  segments, two 80-byte records: rec #2 crosses the boundary, build
  receives prev=80 (the pad's start LSN), rec #2 lands at LSN 128;
  the chain pad → rec#2 is intact.
- `TestStripeAppendBuildConcurrentDisjointStripesProgressInParallel` —
  8 stripes × 50 records each, all 400 start LSNs distinct, tracker
  idle at end.
- `TestStripeWriterCoreAppendBuiltDelegatesToStripeAppendBuild` —
  exercises the `core.AppendBuilt` wrapper end-to-end (reserve →
  build → publish → read back).
- `TestStripeWriterCoreAppendBuiltNilReceiverReturnsError` — nil
  receiver guard.

Verified: `go test -race -count=1 -run
'TestStripeAppendBuild|TestStripeWriterCoreAppendBuilt'
./internal/wal/` PASS (1.04 s); `go test -race -count=1
./internal/wal/` PASS (3.18 s); `go vet ./internal/wal/` clean.

## PG-compat

None — this foundation is an in-memory composer. WAL record / file
format / catalog / wire all unchanged; the encoded record bytes the
build closure returns are identical to what the legacy `state.append`
path emits via `encodeRecordXLog`, just produced under a stripe lock
instead of `state.appendMu`.

## Out of scope (deferred to slice B call-site rewrite parts 2/3)

- Mounting `core.AppendBuilt` as the body of `state.append` for the
  PG-compat path (rewriting the appendMu-protected reserve+encode
  block to use the stripe lock).
- Mounting `core.PublishUpTo` in the drain goroutine's prelude (the
  drain-side counterpart of the writer-side rewrite).
- 8-byte MAXALIGN of record sizes in the Append pre-amble — already
  satisfied by `encodeRecordXLog`'s `maxAlignXLog(realLen)` padding;
  the call-site rewrite will assert the invariant at the boundary.
- Group-commit fast path (`tryAppend`) reroute through the core.
- Walreceiver replay path (`appendRaw`) does not need
  `stripeAppendBuild` — its bytes arrive pre-encoded from the primary
  with the xl_prev chain already stamped. The plain `core.Append`
  (foundation 14) is the right primitive for that site.

## Cross-references

- Parent: `docs/design/perf-optimize/07-wal-fsm-insert.md` §2
- Foundation 14: `0107-0007u-wal-stripe-append.md`
- Foundation 15: `0107-0007v-wal-stripe-writer-core.md`
- Mount: `0107-0007w-wal-stripe-writer-core-mount.md`
- Cross-segment hook: `0107-0007s-wal-segment-pad-emit.md`
- LSN reservation: `0107-0007p-wal-reserve-and-publish.md` (the
  reserveAndPublish whose post-reserve prev this composer feeds into
  build)
