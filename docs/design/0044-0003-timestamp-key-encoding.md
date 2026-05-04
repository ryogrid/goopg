# 0044-0003 — `timestamp` B-tree Key Encoding

**Status:** draft
**Parent milestone:** M0044
**Date:** 2026-05-04

## 1. Objective

Define a fixed-length, sortable byte encoding for
`timestamp without time zone` B-tree keys whose bytewise
comparison matches numeric (chronological) order.

## 2. Underlying representation

PostgreSQL stores `timestamp without time zone` as an
**8-byte signed integer**: number of microseconds since the
PostgreSQL epoch (`2000-01-01 00:00:00`). Negative values
represent times before the epoch. Goopg's runtime `KindTime`
Datum already carries the same scalar (the conversion lives in
`internal/executor/expr.go::evalTypedStringLit` for literals;
the parser and storage layer normalise to microseconds-since-
2000 in lock-step with PostgreSQL).

## 3. Layout

```go
// EncodeTimestamp returns the sortable byte form of a
// timestamp value. The input is the microsecond offset from
// 2000-01-01 00:00:00; negative values are valid.
//
// Layout: 8 bytes big-endian with sign bit flipped, mirroring
// EncodeInt8. Bytewise comparison matches chronological order.
func EncodeTimestamp(microsSince2000 int64) []byte {
    var b [8]byte
    binary.BigEndian.PutUint64(b[:], uint64(microsSince2000)^0x8000000000000000)
    return b[:]
}
```

This is **identical to `EncodeInt8`**. The encoding correctness
proof is the same: big-endian + sign-bit flip puts negative
values before positive values, and within each sign the bit
representation already sorts correctly.

## 4. Sort order

```
'1995-01-01 00:00:00' → microsSince2000=-157766400000000  → 80 ... (high bit cleared by flip)
'1996-01-01 00:00:00' → microsSince2000=-126230400000000
'1998-12-31 23:59:59' → microsSince2000= +31535999000000
```

Bytewise compare gives `1995 < 1996 < 1998` — correct.

## 5. Compound-key composition

The encoding is fixed-length 8 bytes, so it is **trivially
self-terminating** when concatenated. Index on
`(l_shipdate timestamp, l_orderkey numeric)`:

```
('1995-01-15', 1)  → encode_timestamp(...) (8 B) || encode_numeric(1)
('1995-01-15', 2)  → encode_timestamp(...) (8 B) || encode_numeric(2)
('1995-02-01', 1)  → encode_timestamp(...) (8 B) || encode_numeric(1)
```

Bytewise compare yields the SQL multi-column order.

## 6. Implementation

- New helper `EncodeTimestamp(microsSince2000 int64) []byte` in
  `internal/access/btree/btree.go`.
- `encodeBTreeKeyForColumn` learns a `timestamp` /
  `timestamp without time zone` / `timestamptz` case (the last
  one currently unused by HammerDB but trivial to support — same
  bytes since the runtime stores both as microseconds-since-
  2000). For now `timestamptz` will be deferred behind a
  follow-up flag — see § 8.
- The runtime Datum kind that carries the value is `KindTime`;
  the field is `Time int64` (microseconds-since-2000). The
  encoding pulls it directly.
- `isSupportedBTreeKeyType` accepts `timestamp` and
  `timestamp without time zone`. (The HammerDB schema uses
  `TIMESTAMP` which the parser maps to `timestamp without time
  zone`.)

## 7. Edge cases

- **NULL**: rejected at backfill / insert with `42804`, same as
  every other type.
- **Out-of-range**: int64 microseconds-since-2000 already covers
  ±294,000 years from epoch. PostgreSQL's range is
  `4713 BC .. 294276 AD`; goopg uses the same int64 carrier so
  it cannot overflow.
- **Resolution mismatch**: PostgreSQL's literal parser truncates
  sub-microsecond input. Goopg follows the same rule, so two
  literals that PostgreSQL considers equal also produce equal
  encoded bytes.

## 8. `timestamptz` — deliberately deferred

`timestamp with time zone` (`timestamptz`) shares the same
microseconds-since-2000 storage but carries a session-time-zone
display rule. The HammerDB TPC-H schema uses only
`TIMESTAMP` (without time zone). Adding `timestamptz` index
support is a one-line guard relaxation in
`isSupportedBTreeKeyType` once goopg has a session-timezone
story; it is out of scope for M0044's TPC-H-driven goals.

## 9. Verification

- `internal/access/btree/timestamp_key_test.go` — ordering
  assertions including the epoch boundary and a few
  TPC-H-shaped dates (`1992-01-01`, `1995-09-15`, `1998-12-31`).
- Integration test
  `internal/executor/storage_ddl_timestamp_test.go` builds a
  single-column `lineitem (l_shipdate)` index and verifies a
  range-scan over `[1995-01-01, 1996-01-01)` returns exactly the
  rows the SeqScan path would.
- `TestTPCHResultParity` regression gate.
