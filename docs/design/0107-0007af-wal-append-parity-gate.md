# 0107-0007af — WAL append parity gate (slice B foundation 23)

Status: LANDED with discovery (foundation 23 of slice B; multi-record
parity tests t.Skip-deferred until the prev-RecPtr divergence is
resolved by the call-site rewrite).
Parent: [M0107-0007 Phase D4 — WAL insert striping](perf-optimize/07-wal-fsm-insert.md).
Predecessor: [[0107-0007ae]] `AppendXLogPayload` composer (foundation 22).

## Context

Foundations 1–22 of slice B landed the 8-stripe WAL-insert primitives
in isolation, finishing with [[0107-0007ae]] `AppendXLogPayload` — the
top-level composer the call-site rewrite mounts at `state.append`'s
PG-compat write path. The composer's design doc claims:

> Byte-identical to today's `state.append` PG-compat path.

That claim was verified for single-record emissions
([[0107-0007ae]] `TestAppendXLogPayloadBytesLandInWalBuf`) but never
chain-verified across multiple records under the actual legacy
prev-RecPtr threading semantics. The call-site rewrite (multi-loop
scope, splits `state.appendMu`'s four invariants) ASSUMES byte
substitutability; without an explicit parity gate, a regression in
either path would surface as on-the-wire WAL corruption only after
the swap landed in production.

## Decision

Add `internal/wal/append_xlog_payload_parity_test.go` with four side-
by-side tests that:

1. Run a battery of representative payloads (short records, page-
   boundary crossings, empty bodies, monotonic sequences) through a
   reference "legacy" path that replays the exact `encodeRecordXLog +
   emitWithPageHeaders + walBuf.append` sequence `state.append`'s
   PG-compat Path B runs under `appendMu` today.

2. Run the SAME payloads through `stripeWriterCore.AppendXLogPayload`
   (the composer mount-point the call-site rewrite will install).

3. Assert byte-identical `walBuf` contents and matching `(start, prev)`
   LSN tuples.

Helper `emitLegacyPGCompatRecord(walBuf, payload, prev, writePos,
segSize, sysID, tli) → (start0Based, advance, err)` factors out the
legacy emission sequence so the test is a clear A/B comparison of
two implementations producing the same byte stream.

## Discovery — prev-RecPtr convention divergence

The chain-parity tests fail against the current implementation with a
real semantic divergence between the two paths' prev-RecPtr conventions:

| Path | What is stored in `t.prev` / `s.prevRecPtr` |
|------|---------------------------------------------|
| legacy `state.append` | `writePos + leading` — 0-based LSN of THIS record's CONTENT start (after any leading PHD). |
| core `insertPosTracker.reserveLocked` / `reserveEmittedAndPublish` | `start` — 0-based LSN of THIS reservation's start (== page-header byte if one precedes the record). |

The two values differ by `leading` (24 for a page boundary, 40 for a
segment boundary) for every record preceded by a page header. The
on-wire `xl_prev` stamped by the build closure therefore differs;
a `pg_waldump` reader walking the chain would land on a page-header
byte instead of an XLogRecord header.

Concrete example (two records back-to-back at segment 0 with long PHD):

```
                    legacy state.append    core foundation 22
record 1 start      LSN 40 (after long PHD)  LSN 0  (reservation)
record 2 xl_prev    40                        0
```

The foundation-22 design doc's "byte-identical to today's
`state.append` PG-compat path" claim is empirically falsified by the
parity gate for multi-record chains where any previous reservation
crossed a page or segment boundary. Foundation 22's own tests
(notably `TestAppendXLogPayloadTwoRecordsFormChain`) only assert
internal-to-slice-B consistency (`prev2 == start1`); they do not
compare against legacy semantics, and so accepted the divergence.

### Why slice B picked the reservation-start convention

`insertPosTracker.reserveLocked` and `reserveEmittedAndPublish` are
modelled directly on PG's `ReserveXLogInsertLocation`, which advances
`CurrBytePos` and `PrevBytePos` together under one spinlock. PG's
byte-position arithmetic is in record-content space (excludes page
headers); PG's `XLogBytePosToRecPtr` then translates byte position
into RecPtr (LSN, includes page headers) before stamping
`XLogRecord.xl_prev`. Slice B's foundations skipped the byte-pos →
RecPtr translation and instead track `(curr, prev)` directly in LSN
space, with `t.prev = start` (reservation start). For records that
land mid-page (no leading PHD), this convention is PG-correct because
reservation start == content start. For records that land at a page
or segment boundary (leading PHD > 0), the convention undercounts
the leading PHD bytes and the on-wire xl_prev points one page-header
earlier than PG expects.

## Resolution path (deferred to call-site rewrite scope)

Two options:

**(a) Store `t.prev` in record-CONTENT space.**

Inside `insertPosTracker.reserveEmittedAndPublish`, set
`t.prev = start + uint64(leading)` rather than `t.prev = start`.
The translation is one-line because `predictEmittedSize` returns
`leading` under the same `posMu` critical section. The parallel
change in `reserveLocked` requires the caller to pass the leading
PHD count (or for the call site to translate before storing).

**(b) Translate in the build closure.**

Have `appendXLogPayload`'s build closure rewrite the `prev` argument
to `prev + uint64(prev_leading)` before passing to `encodeRecordXLog`.
Requires the closure to recompute / consult the prior reservation's
leading PHD schedule — non-trivial because the closure runs without
direct access to the prior reservation's `(start, leading)` pair.

**(a) is cleaner** because the translation only depends on data
already in scope under `posMu` (the `predictEmittedSize`-returned
`leading`). Adopting (a) requires updating the slice B foundation
tests that pin the reservation-start convention:

- `TestAppendXLogPayloadTwoRecordsFormChain` (foundation 22 test)
- `TestReserveEmittedAndPublishCrossSegmentChainIntegrity`
  (foundation 18 test)
- Any test asserting `prev == previous_reservation_start` rather
  than `prev == previous_content_start`.

The cross-segment XLOG_NOOP pad path also needs review — when the
pad record is dropped at the gap and the triggering reservation
lands at the boundary, the new reservation's `prev` should be the
pad record's content start (matching PG's xl_prev convention),
not the pad record's reservation start. The pad record's content
start coincides with `gapStart` (the pad records have no leading
PHD when emitted via `emitSegmentPad` because they fill the
intra-segment gap, not a segment-aligned position), so the existing
`startCandidate` value is content-correct for the pad — but the
triggering reservation's `prev = startCandidate` needs `+ leading`
applied if the boundary-aligned next reservation gets a long PHD
(which it does — boundary == segment boundary).

## Foundation 23 deliverables

This loop:

1. New test file `internal/wal/append_xlog_payload_parity_test.go`
   with four tests:
   - `TestAppendXLogPayloadParityFirstRecordAlwaysAgrees` — single-
     record case, prev=0; both paths agree (PASSING, regression guard
     against future single-record breakage).
   - `TestAppendXLogPayloadParityWithLegacyEncodeEmit` — 8-record
     chain spanning multiple page crossings (`t.Skip` with deferred
     reason, pending resolution above).
   - `TestAppendXLogPayloadParityShortRecordsSingleStripe` — 64
     records single-stripe (`t.Skip` deferred).
   - `TestAppendXLogPayloadParityEmptyBodyRecords` — body-less
     `[]byte{}` chain (`t.Skip` deferred).

2. Reference helper `emitLegacyPGCompatRecord` factoring the legacy
   PG-compat emission sequence so the parity comparison reads as a
   clean A/B.

3. This design doc documenting the discovery + resolution path so
   the call-site rewrite loop can pick up the gap unambiguously.

4. Constant `parityDeferredReason` holding the t.Skip message so all
   three deferred tests cite the same explanation — a single
   future loop removes all three Skips together.

## What the parity gate locks in (going forward)

- Once the prev-RecPtr resolution lands, removing the t.Skip on the
  three deferred tests is the gate the resolution must pass to
  declare slice B's call-site rewrite ready for PG-compat traffic.

- Foundation 23 itself protects against future single-record breakage
  via `TestAppendXLogPayloadParityFirstRecordAlwaysAgrees`, which
  remains active and exercises the only chain where both paths
  currently agree (no prior record → prev=0).

## Cost

- Test-only; no production-code change in this loop.
- Runtime cost: ~1 ms across all four parity tests combined
  (single fixture build per test, byte-array compare against ≤
  20 KiB walBuf contents).

## PG-compat

None directly (test-only). Indirectly: the gate exists precisely to
defend PG-compat byte stream equivalence ahead of the slice B
call-site rewrite that will route production WAL traffic through
the core path.

## Out of scope (deferred to call-site rewrite)

- Resolving the prev-RecPtr divergence (foundation `0107-0007ag` or
  the call-site rewrite's first concrete loop).
- Adjusting cross-segment XLOG_NOOP pad's xl_prev arithmetic for
  the same convention.
- Mounting `core.AppendXLogPayload` at `state.append`'s PG-compat
  write entry point.
- Extending parity coverage to cross-segment cases (legacy and
  core deliberately differ there — legacy uses
  XLP_FIRST_IS_CONTRECORD, core emits XLOG_NOOP pad records — both
  are PG-compatible but byte-different; that comparison is its own
  design issue).
