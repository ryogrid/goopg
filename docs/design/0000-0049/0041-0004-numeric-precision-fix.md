# 0041-0004 — NUMERIC Precision Fix (Q1, Q8, Q14)

**Status:** landed
**Parent milestone:** M0041
**Date:** 2026-05-04

## 1. Objective

Close the last three TPC-H parity divergences fundamentally — Q1
(`avg(numeric)`), Q8 (`sum/sum`), Q14 (`100·sum/sum`) — by matching
upstream PostgreSQL's NUMERIC division precision exactly, rather
than allowlisting them as "numeric precision delta". After this
landing, `TestTPCHResultParity` passes with **identical=22,
divergent=0, errored=0** and the `knownDivergences` allowlist is
empty.

## 2. Two root causes

### 2.1 `numericDiv` scale rule

The pre-fix rule (`internal/executor/numeric.go:189-225`) was
`target = max(left.scale, right.scale, 6)` — a hardcoded floor of 6
fractional digits, then truncating integer division via
`big.Int.Quo`. Upstream's rule (`postgres/src/backend/utils/adt/
numeric.c:10135-10195`, `select_div_scale`) is

```
rscale = max(NUMERIC_MIN_SIG_DIGITS(16) - qweight*DEC_DIGITS(4),
             dscale1, dscale2, 0)
rscale clamped to [0, NUMERIC_MAX_DISPLAY_SCALE(1000)]
```

where `qweight = (weight1_NBASE - weight2_NBASE) - (firstdigit1_NBASE
≤ firstdigit2_NBASE ? 1 : 0)` and weights are computed in NBASE=10000
(four decimal digits per NBASE digit). This produces ~17 significant
digits — float8-equivalent precision, what every TPC-H result row
expects.

The rounding mode is **half-away-from-zero**, not truncation —
upstream's `div_var(rounding=true)`. The pre-fix `Quo` truncation
was a separate parity bug.

### 2.2 int64 mantissa overflow

Q8's expected output `1.00000000000000000000` requires
mantissa = 10^20 ≈ 1 × 10^20, which exceeds max int64
(≈ 9.2 × 10^18). Any rscale ≥ 19 with a non-trivial integer part
overflows the previous `int64 NumericMantissa` carrier.

## 3. Fix

### 3.1 Hybrid `*big.Int` overflow lane in Datum

`internal/executor/datum.go`:

- `Datum` gains a new `NumericBig *big.Int` field. When non-nil,
  the value is `NumericBig × 10^-NumericScale` and `NumericMantissa`
  is ignored. When nil, the int64 fast path applies as before.
- `NumericScale` widens from `int8` to `int16` to cover upstream's
  `NUMERIC_MAX_DISPLAY_SCALE = 1000`.
- `Format()` dispatches through new `numericText(d Datum)` to
  `formatNumericBig` when the big lane is active, else
  `formatNumeric`.

Why hybrid (not full `*big.Int`):

- TPC-H scans tens of millions of rows; only the per-group division
  result needs wide precision. Per-row fast path stays on int64,
  preserving hash-join key encoding and B-tree leaf encoding
  byte-for-byte.
- Heap on-disk format is varlen text via `formatNumeric` /
  `parseNumeric` — widening those two helpers is sufficient. **No
  on-disk migration required.**
- Hybrid keeps the diff localised. Two helpers in `numeric.go`
  quarantine the lane decision:
  - `numericMant(d Datum) *big.Int` — read accessor.
  - `newNumeric(b *big.Int, scale int) Datum` — constructor; lands
    on int64 lane when `b` fits, big lane otherwise.

### 3.2 New `numericDiv`

`internal/executor/numeric.go`:

- `parseNumeric` returns `(*big.Int, int16, error)` (was
  `(int64, int8)`); accumulates digits in `*big.Int`. Drops the
  "numeric out of int64 range" error path — wider literals now
  succeed.
- `numericAdd`, `numericSub`, `numericMul`, `numericCmp` use
  `*big.Int` internally and emit through `newNumeric`.
- `numericDiv` mirrors upstream's `div_var` + `select_div_scale`:
  - Computes `qweight` and rscale via `numericDivScale`, which uses
    `nbaseWeightAndFirstDigit` to pack mantissas into NBASE=10000
    (4 decimal digits per NBASE digit) — necessary because
    upstream's formula counts NBASE-aligned chunks, not single
    decimal digits. The pure-decimal-digit-with-DEC_DIGITS=1
    interpretation gives off-by-one rscale on Q1's 70/6 (15 vs 16).
  - Multiplies numerator by `10^shift` (where `shift = rscale +
    db - da`), then `QuoRem` against denominator.
  - Rounds half-away-from-zero by comparing `2*|rem|` to `|den|`.

### 3.3 Codec / spill / B-tree

- **Heap codec (`internal/executor/codec.go`)** — varlen text.
  The encode arm calls new `numericText(d)`, which transparently
  handles either lane. The decode arm goes through `newNumeric`.
  No on-disk format change.
- **Spill (`internal/executor/spill.go`)** — KindNumeric layout
  bumped to `int16 scale + uint32 length + sign byte + magnitude
  bytes`. Per-query spill files have no on-disk back-compat
  constraint.
- **B-tree key (`internal/access/btree/btree.go`)** —
  `EncodeNumericKey` signature changes to
  `(*big.Int, int16) []byte`. The on-page byte layout is
  preserved (sign + biased exp + digits + terminator). Sort order
  is unchanged because the encoding is variable-length and the
  digit-rebase trick already handles arbitrary digit counts.
- `MaxHighKeyLen` stays at **32** (would-have-been-bumped reverted
  — TPC-H NUMERIC index keys all fit in int64, and the
  arbitrary-precision lane only matters for runtime arithmetic
  results that never enter an index; bumping the on-page opaque
  area would require a pageformat migration, which the cost
  doesn't justify).

### 3.4 Allowlist removal

`internal/testutil/tpch/parity_test.go:176` — `knownDivergences`
shrunk to `map[int]string{}`. The gate now demands all 22 queries
IDENTICAL, with no precision exceptions.

## 4. Verification

| Test | Result |
|------|--------|
| `TestNumericDivQ1Avg` | PASS — `70/6 = "11.6666666666666667"` (16 digits) |
| `TestNumericDivQ8MktShare` | PASS — `1.0/1.0 (scale 20) = "1.00000000000000000000"` (big-lane) |
| `TestNumericDivRoundHalfAwayFromZero` | PASS — `5/2 = "2.5000000000000000"` |
| `TestRunTPCHQueriesAgainstSyntheticData` | 22/22 PASS |
| `TestTPCHResultParity` | **identical=22 divergent=0 errored=0 — PASS** |
| `go test ./...` | clean (only pre-existing `tmp/` scratch dir error) |

## 5. References

- Upstream PG NUMERIC: `postgres/src/backend/utils/adt/numeric.c`
  - `numeric_div` (lines 3243-3251) → `numeric_div_opt_error` →
    `select_div_scale` + `div_var`
  - `select_div_scale` lines 10135-10195
  - `numeric_avg` final divide line 6278
- Upstream constants: `postgres/src/include/utils/numeric.h`
  - `NUMERIC_MIN_SIG_DIGITS = 16` (line 50)
  - `NUMERIC_MIN_DISPLAY_SCALE = 0`, `NUMERIC_MAX_DISPLAY_SCALE = 1000`
- goopg pre-fix design: `docs/design/0003-0012-numeric-arithmetic.md`
  (the "deferred to type system" note for division is now obsolete
  for the precision-correctness aspect — performance work on
  per-row big.Int allocation is a separate concern).
