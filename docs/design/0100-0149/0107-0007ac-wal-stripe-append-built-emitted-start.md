# 0107-0007ac — WAL stripe-append: pass `start` LSN to the build closure

Foundation 20 for M0107-0007 slice B (Phase D4 — 8-stripe WAL insert locks
per `docs/design/perf-optimize/07-wal-fsm-insert.md` §2).

## Problem

Foundation 19 ([[0107-0007ab]] `stripeAppendBuiltEmitted`) defined the
build closure as

```go
build func(prev uint64, total, leading int) ([]byte, error)
```

passing only `(prev, total, leading)`. The intended caller in the slice B
call-site rewrite is `state.append`'s PG-compat path, which today calls
`emitWithPageHeaders(record, realRecLen, writePos, segSize, sysID, tli)`
to interleave page headers with the record body. `emitWithPageHeaders`
requires `startPos` (the reservation's start LSN within the WAL byte
axis) — it computes page boundaries, the contrecord split point, and the
system-ID / timeline stamped into each header from that position.

Without `start` in the closure, the rewrite cannot call
`emitWithPageHeaders` from inside the composer; the only workaround
would be to predict the start outside the composer (re-opening the
predict-vs-reserve race that foundation 18 ([[0107-0007aa]]
`reserveEmittedAndPublish`) closed) or to reach into `posTracker.curr`
under `posMu` (which the composer already holds — there is no clean
non-API surface for that).

## Fix

Thread `start` through to the closure. The new signature is

```go
build func(start, prev uint64, total, leading int) ([]byte, error)
```

`start` is the LSN returned by `reserveEmittedAndPublish` — already
known inside the composer post-reservation; passing it costs nothing.

Cross-segment crossings: `reserveEmittedAndPublish` returns the
**post-boundary** start (the LSN where the triggering reservation
lands, NOT the pad's start LSN), and the closure receives that
post-boundary value. This is the right input for
`emitWithPageHeaders`: the pad record's bytes are emitted synchronously
by `emitSegmentPad` ([[0107-0007s]]) under posMu during
`onCrossSegment`; the build closure's record begins at the boundary
with a long PHD.

## Why now

The slice B call-site rewrite is the next step after foundation 19.
That rewrite needs the closure to be callable as

```go
core.AppendBuiltEmitted(procNum, recordLen, func(start, prev uint64, total, leading int) ([]byte, error) {
    record, _, err := encodeRecordXLog(payload, prev)
    if err != nil { return nil, err }
    out, _ := emitWithPageHeaders(record, recordLen, int64(start), int64(segSize), sysID, tli)
    return out, nil
})
```

Without foundation 20 the rewrite has to either duplicate
predict-and-reserve logic (race-prone) or expose internal fields. The
signature change is one-line; the impact is one production caller
shape change (the future call-site rewrite) and tests.

## Scope

- `stripeAppendBuiltEmitted` in `internal/wal/stripe_append_emitted.go`:
  closure signature extended; doc comment updated to describe the new
  contract.
- `(*stripeWriterCore).AppendBuiltEmitted` in
  `internal/wal/stripe_writer_core.go`: matching signature.
- Tests in `internal/wal/stripe_append_emitted_test.go`: existing closures
  updated to take the new `start` parameter (mostly placeholder `_`).
  The happy-path test now asserts the closure observes
  `start == 0` on the first reservation and `start == total1` on the
  second. The cross-segment test asserts the closure observes
  `start == segSize` (the post-boundary value), pinning the contract
  that the closure NEVER sees the pre-shift candidate start.

## Out of scope

- Mounting `core.AppendBuiltEmitted` as the body of `state.append`
  (the slice B call-site rewrite proper, multi-loop scope —
  `state.appendMu`'s four invariants split into per-stripe local
  state vs. shared state).
- Mounting `core.PublishUpTo` in the drain goroutine's prelude
  (`drainBufferBytes` currently runs under `appendMu`).
- 8-byte MAXALIGN of record sizes in the Append pre-amble (the
  call-site rewrite's pre-step; `encodeRecordXLog` already produces
  MAXALIGN-padded records, so the rewrite will assert the invariant
  at the boundary rather than enforce it here).

## Verification

- `go test -race -count=1 -run
  'TestStripeAppendBuiltEmitted|TestStripeWriterCoreAppendBuiltEmitted'
  ./internal/wal/` PASS (1.04 s).
- `go test -race -count=1 ./internal/wal/` PASS (4.09 s).
- `go vet ./internal/wal/` clean.

## PG-compat

None — this is an in-memory composer signature change. The byte stream
the build closure produces is identical to what the legacy
`state.append` path emits via `encodeRecordXLog + emitWithPageHeaders`;
only the closure's argument shape changed.

## References

- [[0107-0007ab]] foundation 19 — original `stripeAppendBuiltEmitted`
  with `(prev, total, leading)` closure.
- [[0107-0007aa]] foundation 18 — `reserveEmittedAndPublish` (the
  posMu-held predict+reserve+publish primitive that determines `start`).
- [[0107-0007z]] foundation 17 — `predictEmittedSize` (consumed by
  foundation 18; pure size arithmetic).
- [[0107-0007v]] foundation 15 — `stripeWriterCore` (the packaging
  struct that owns the four slice B primitives and exposes
  `AppendBuiltEmitted`).
- [[0107-0007u]] foundation 14 — `stripeAppend` (the size-explicit
  composer; `stripeAppendBuild` and `stripeAppendBuiltEmitted` are
  encode-after-reserve siblings).
- `internal/wal/xlog_emit.go:emitWithPageHeaders` — the consumer that
  motivated the `start` argument.
