# 0107-0007ad — `predictXLogRecordLen` pure encodeRecordXLog size mirror (M0107-0007, slice B foundation 21)

Status: accepted (2026-05-21)
Parent: [[07-wal-fsm-insert]] (M0107-0007)
Slice: B foundation 21 / N
Precedents:
- [[0107-0007y]] `stripeAppendBuild` — composer that needs an exact
  reservation size.
- [[0107-0007z]] `predictEmittedSize` — pure mirror of
  `emitWithPageHeaders`' byte arithmetic.
- [[0107-0007aa]] `reserveEmittedAndPublish` — closes the
  predict-vs-reserve race.
- [[0107-0007ab]] `stripeAppendBuiltEmitted` — joint composer that consumes
  `recordLen` (the MAXALIGN-padded encoded length).
- [[0107-0007ac]] build closure receives `start` LSN.

## Problem

The slice B call-site rewrite of `state.append` / `state.tryAppend` for the
PG-compat path consumes [[0107-0007ab]] `core.AppendBuiltEmitted(procNum,
recordLen, build)` where `recordLen` MUST equal `len(encodeRecordXLog(payload,
prev))`. Two prior approaches both fail:

1. **Encode-then-reserve**: call `encodeRecordXLog(payload, 0)` outside the
   composer to learn the length, then call `AppendBuiltEmitted(procNum,
   paddedLen, ...)`. The build closure must re-encode with the real `prev`
   because the throwaway encode used `prev=0`. Two encodes per insert is a
   2× allocation tax on the hot path.

2. **Stash the encoded record**: encode once outside the composer with
   `prev=0`, then patch `xl_prev` inside the build closure. PG's
   `EncodeXLogRecordHeader` includes the CRC over the header; patching
   `xl_prev` post-encode means recomputing the CRC, which means re-running
   the same arithmetic that `encodeRecordXLog` already does internally. The
   complexity gain is zero.

## Solution

A pure mirror of `wrapXLogMainData` + `encodeRecordXLog`'s byte arithmetic
that returns the (realRecLen, paddedLen) pair without allocating or
encoding.

```go
func predictXLogRecordLen(payload []byte) (realRecLen, paddedLen int)
```

- `realRecLen` = `xlogRecordHeaderSize + wrappedLen` (the value stamped
  into `XLogRecord.TotLen`).
- `paddedLen` = `maxAlignXLog(realRecLen)` (the byte count
  `encodeRecordXLog` actually returns from its `make([]byte, ...)`).

`wrappedLen` branches mirror `wrapXLogMainData`:

| Condition | wrappedLen |
|---|---|
| `payload[0] == 0xFE && len ≥ 7` (M0106-0010 canonical envelope) | `len(payload) - 7` |
| `len(payload) ≤ 0xFF` (PG `xlrBlockIDDataShort`) | `2 + len(payload)` |
| `len(payload) > 0xFF` (PG `xlrBlockIDDataLong`) | `5 + len(payload)` |

The function is a no-allocation arithmetic mirror — both `realRecLen` and
`paddedLen` derive from `len(payload)` and (for the canonical branch)
`payload[0]` only.

## Pairing for the slice B call-site rewrite

```go
realRecLen, paddedLen := predictXLogRecordLen(payload)
start, prev, _, _, err := core.AppendBuiltEmitted(
    procNum, paddedLen,
    func(start, prev uint64, total, leading int) ([]byte, error) {
        record, _, err := encodeRecordXLog(payload, prev)
        if err != nil {
            return nil, err
        }
        out, _ := emitWithPageHeaders(record, realRecLen,
            int64(start), s.cfg.SegmentSize, s.sysID, s.tli)
        return out, nil
    })
```

The build closure encodes exactly once with the real `prev` returned by
`reserveEmittedAndPublish` under posMu. `realRecLen` is captured by the
closure (no second predict call needed inside).

`AppendBuiltEmitted`'s contract requires `len(out) == total` — the
combination `len(encodeRecordXLog(...).out) == paddedLen` and
`len(emitWithPageHeaders(..., paddedLen, ...)) == total` is structurally
guaranteed because `total` was computed by `predictEmittedSize(paddedLen,
start, segSize)` inside `reserveEmittedAndPublish` ([[0107-0007aa]]) and
`predictEmittedSize` is the byte-arithmetic mirror of
`emitWithPageHeaders`.

## Invalid input

`payload == nil` returns `(0, 0)`. `encodeRecordXLog` itself produces a
non-zero result for nil (32-byte header + 2-byte short chunk = 34 bytes),
but no real caller passes nil; the guard surfaces a bug as a structured
`errStripeAppendEmptyRecord` from `AppendBuiltEmitted` instead of a silent
zero-size reservation.

## Out of scope

- Mounting at the call site. The slice B call-site rewrite for
  `state.append` / `state.tryAppend` / `state.appendBatch` consumes this
  helper plus the four prior foundations (17–20); the rewrite itself is
  multi-loop because `state.appendMu`'s four invariants — writePos /
  walBuf / memRing / writeLSN — split into per-stripe local state vs.
  shared state, and `drainBufferBytes` must move out from under appendMu
  before the stripe-concurrent flow is safe.
- Walreceiver replay (`appendRaw`). Bytes arrive pre-encoded from the
  primary with the xl_prev chain already stamped, so that path consumes
  the size-explicit [[0107-0007p]] `reserveAndPublish` /
  [[0107-0007u]] `stripeAppend`, not the predict-driven
  `AppendBuiltEmitted`.

## PG-compat

None. Pure size-prediction mirror; produces no bytes, does not interact
with on-disk WAL record format, file format, catalog, or wire protocol.

## Tests

`internal/wal/predict_xlog_record_len_test.go` (5 tests):

- `TestPredictXLogRecordLenMatchesEncodeRecordXLog` — 12-case matrix
  covering empty, odd lengths (MAXALIGN exercise), the 0xFF/0x100
  short→long wrapping boundary, and the 0xFE canonical-envelope branch.
  Compares `predictXLogRecordLen` output to `encodeRecordXLog`'s actual
  `(realRecLen, len(encoded))`. The two share zero implementation surface
  so agreement detects drift in either direction.
- `TestPredictXLogRecordLenPaddedIsMaxAlignOfReal` — pins `paddedLen ==
  maxAlignXLog(realRecLen)` for all payload sizes 0..64. Any future
  change to `xlogRecordAlign` or `maxAlignXLog` ripples to both call
  sites atomically.
- `TestPredictXLogRecordLenCanonicalShortCircuitsFirstByte` — pins the
  three-way branch dispatch: canonical envelope (0xFE + 7+ bytes), short
  wrap (≤ 0xFF), too-short payload starting with 0xFE (falls through to
  short wrap because canonical requires the 7-byte header).
- `TestPredictXLogRecordLenShortLongBoundary` — explicit 0xFF vs 0x100
  case pinning the 4-byte delta between short-wrap (2-byte header) and
  long-wrap (5-byte header).
- `TestPredictXLogRecordLenNilPayloadReturnsZero` — defensive
  short-circuit; matches the foundation-level nil-safety convention
  from [[0107-0007l]] / [[0107-0007o]] / [[0107-0007q]].
- `TestPredictXLogRecordLenIsPureNoSideEffects` — pins that the
  function does not mutate its payload argument (the slice B call site
  holds the payload across the reservation→build closure boundary).

Verified:
- `go test -race -count=1 -run 'TestPredictXLogRecordLen' ./internal/wal/` PASS (1.02 s)
- `go test -race -count=1 ./internal/wal/` PASS (4.12 s)
- `go vet ./internal/wal/` clean
