# 0107-0013 — WAL ring-window admission check inside posMu (M-NIGHTLY)

Status: accepted
Milestone: M-NIGHTLY (AI-20260916-035206-008, `testport/TestPort_IsolationSuite`)
Related: [[0107-0007ai]] (head/base atomic drain), [[0107-0007aa]]
(`reserveEmittedAndPublish`), [[0107-0007n]] tailPublisher,
[[0107-0012]] (idle-sentinel + Path A appendMu discipline),
[[0107-0011]] (test-only drain artifact),
[[0013-0001]] (wal buffers architecture).

## Symptom

`TestPort_IsolationSuite` under sustained multi-backend load (and the
nightly run of it, which timed out at 7200 s mid-suite) produced:

- `wal: writeReserved range outside buffer window` errors surfacing to
  clients as deferred spec failures (`side=over-window`),
- `backend goroutine panic` storms —
  `stripeWriterCore: cross-segment pad emit failed: wal: writeReserved …`
  — recovered per-connection at `serveConn.func1`, each one killing a
  DDL/setup statement mid-flight and cascading into catalog-mirror drift
  (`dst extend produced blk N, expected M`, `relation "foo" already
  exists`),
- a terminal `readForDrain` panic (`slice bounds out of range` —
  `tail - head > cap`) that wedged the whole server.

## Root cause — two compounding defects

**Defect 1 — under-sized capacity claim.** `tryAppend`/`appendPGCompat`
claimed `2*(paddedLen+64)` ring bytes before calling
`AppendXLogPayload`, assuming the emitted record fits `paddedLen` plus
one page header. `predictEmittedSize` actually interleaves a page
header at *every* 8 KiB boundary, and a segment-boundary crossing emits
`gap + total` bytes (pad over `[curr, boundary)` plus the re-landed
record). For records spanning ≥3 pages, `gap + total` can exceed
`2*(paddedLen+64)` — the claim under-budgeted the real footprint.

**Defect 2 — the claim accounting cannot express the real invariant.**
`tryReserve` admits a claim while `resident + reserved ≤ cap`, i.e.
`tail + reserved ≤ head + cap`. The invariant `writeReserved` actually
needs is `start + footprint ≤ head + cap`, which follows only when
`curr ≤ tail + reserved` — "every unpublished byte carries a live
claim". Three independent mechanisms broke that invariant:

- *Capped publish + unconditional release.* `tryAppend`'s success path
  ran `PublishUpTo(end)` then `releaseReservation(claim)`. `PublishUpTo`
  caps at `lowestActiveLSN`: while a slower stripe holds a lower active
  LSN, `tail` stops below our end, yet the claim is still released —
  `curr - tail` keeps counting our range but `reserved` no longer does.
  A purely transient hole on the *success* path, sized by however much
  LSN sat above the blocking stripe's start (observed ~1.3 MB).
- *Post-reserve error return.* `AppendXLogPayload` errors after
  `reserveEmittedAndPublish` commits `curr` left a burned LSN range;
  the caller released the claim without publishing → permanent
  uncounted hole. `Writer.Append` then silently fell through to the
  slow path, hiding the first failure entirely.
- *MemRing eviction as a bogus error source.* `MemRing.WriteReserved`
  can legitimately fail (`errMemRingReservedOutOfRange`): a peer
  stripe's `AdvanceWindow(end_p)` slides `memRing.head` past a pending
  write's start between the writer's own `AdvanceWindow` and
  `WriteReserved` — the two calls are not atomic. The memRing is a
  walsender read cache with a disk fallback; treating a cache miss as
  an append failure converted a harmless miss into a hole (or, inside
  `emitSegmentPad`'s hook, into a backend panic that killed the
  connection mid-DDL).

Once `curr - tail > reserved`, `tryReserve` keeps granting claims for
space `curr` has already consumed; the next `writeReserved` overshoots
`head + cap`, errors, and (pre-fix-3) creates yet another hole — a
self-feeding cascade ending in `resident > cap` → `readForDrain`
panic.

## Fix

1. **Claim sizing.** `walBufferReservationClaim(recordLen, segSize)` =
   `2 * predictEmittedSize(recordLen, 0, segSize)` — `gap < total` and
   `total` is maximised at a segment-aligned start, so
   `gap + total < 2*maxEmitted` always. Applied at both `tryAppend`
   and `appendPGCompat`.

2. **Window admission inside posMu.** `insertPosTracker` gained
   `windowEndFn` (wired by `newStripeWriterCore` to
   `walBuf.head + cap`). `reserveEmittedAndPublish` now checks
   `start + total ≤ windowEndFn()` — for a crossing, `boundary +
   total_boundary` — *inside* `posMu`, before `curr` commits and before
   the pad is emitted. Refusal returns `ok=false`; the composer maps it
   to `walBufferCapacityExceeded`, which `tryAppend` releases its claim
   for and reports as retryable (`ok=false`), and which Path B handles
   by publish-all + drain-all + retry inside its claim loop. `head` is
   monotonic, so a reservation admitted under the check can never fail
   `writeReserved`'s range check at write time — the overshoot failure
   mode is structurally eliminated, independent of claim-accounting
   transients.

3. **MemRing misses are cache misses, not failures.** Both
   `stripeAppendBuiltEmitted` and `emitSegmentPad` now skip a failed
   `memRing.WriteReserved` instead of propagating it — walsenders fall
   back to reading the segment file.

4. **Burned-reservation healing.** Any remaining `AppendXLogPayload`
   error (build failure, size mismatch — programmer-bug paths) now
   zero-fills `[start0, start0+total)` (best-effort), publishes over
   the burn, then releases the claim — so even a residual failure
   cannot wedge the ring, and the drained bytes are durable zeros
   (clean end-of-WAL for replay) rather than stale ring content that
   might decode as a valid record.

5. **`Writer.Append` propagates `tryAppend` errors** instead of
   silently falling through to the slow path; only `ok=false, err=nil`
   (claim/Window overflow) still falls through to `state.append`.

## What did not change

- `appendMu` RLock/Lock discipline, `writeMu` ordering, Path A's
  exclusive section, `lowestActiveLSN` publish capping, drain/readForDrain
  mechanics — all unchanged.
- `tryReserve`/`releaseReservation` claims remain as admission control;
  they no longer carry the safety invariant alone.
- `MemRing.WriteReserved` still returns `errMemRingReservedOutOfRange`;
  only the two composer call sites demote it.
- `emitSegmentPad`'s `walBuf` pad-write error still propagates to the
  panicking `onCrossSegment` closure — unreachable now that admission
  is window-checked, kept as a loud never-happen guard (a failed pad
  breaks the xl_prev chain).

## Tests

- `wal_buffer_reservation_claim_test.go` — claim ≥ worst-case
  `gap + total` sweep; maximal emitted size at aligned starts;
  deterministic near-full cross-segment append.
- `wal_reserved_window_stress_test.go` — 12 appenders × 4 flushers,
  1 MiB ring / 256 KiB segments, multi-page payloads, 3 s — zero
  append errors.
- `TestStripeAppendBuiltEmittedMemRingEvictionDoesNotFailAppend` —
  forces `memRing.head` past the next reservation's start; append
  succeeds and walBuf bytes still land.
- `TestEmitSegmentPadSkipsMemRingOutOfWindow` — pad emit over an
  evicted memRing window returns nil (was: error → panic).
- `go test ./internal/access/transam/xlog/` and `-race`: green.
- `TestPort_IsolationSuite`: zero `writeReserved` errors and zero
  backend panics across the full run (previously dozens plus a server
  wedge).
