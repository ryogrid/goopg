# Design 0074-0006 — Numeric int64 fast-path in compareDatum / arithmetic

**Milestone:** M0074-0006
**Status:** draft
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0073-0001 — Datum struct (lifecycle for
NUMERIC int64 fast-path lane already documented at
datum.go:93-95).

## Context

Q5 CPU pprof at M0073-final shows `compareDatum` at
5.86 % flat / 12.17 % cum. Reading the code path:

```
compareDatum (expr.go:421-494)
  → promoteCrossKind (NUMERIC promotion)
  → numericCmp (numeric.go:416-425)
    → numericMant(a) → fresh big.Int allocation
    → numericMant(b) → fresh big.Int allocation
    → alignNumericBig → big.Int operations
    → big.Int.Cmp
```

`numericMant` (numeric.go:34-39) **always** allocates a
fresh `*big.Int` regardless of operand kind:

```go
func numericMant(d Datum) *big.Int {
    if d.NumericBigValue() != nil {
        return new(big.Int).Set(d.NumericBigValue())
    }
    return big.NewInt(d.NumericMantissaValue())
}
```

For TPC-H workloads (lineitem.l_quantity NUMERIC(15,2),
l_extendedprice NUMERIC(15,2), l_discount NUMERIC(15,2),
l_tax NUMERIC(15,2)), all mantissas fit easily in int64.
`big.NewInt` is a heap allocation per call (~24 B). Q5
performs millions of comparisons over filtered lineitem
rows.

Datum already has the int64 fast-path lane at storage
(datum.go:93-95): `KindNumeric — Big != nil → big.Int
mantissa, else Int = int64 mantissa fast path`. The
arithmetic / comparison helpers don't exploit it.

## Goals

- Add int64 fast-path arms in `numericCmp`, `numericAdd`,
  `numericSub`, `numericMul`, `numericDiv`. Skip
  `big.Int` allocation when both operands have
  `Big == nil` and the aligned mantissas fit in int64.
- New helpers:
  - `numericCmpInt64Fast(am int64, ascale int16, bm int64,
                          bscale int16) (cmp int, ok bool)`
  - `alignNumericInt64(am int64, ascale int16, bm int64,
                         bscale int16) (am2 int64, bm2 int64,
                         ok bool)` — `ok=false` on overflow.
  - `mulInt64Pow10(v int64, exp int) (result int64,
                     ok bool)` — overflow-checked.
- Result-correctness: int64 fast-path output must match
  big.Int slow-path bit-for-bit on every input pair where
  the fast path returns `ok=true`.

## Non-goals

- **Replacing `big.Int` slow path.** Big mantissas (NUMERIC
  precision > 18 digits) still go through big.Int.
- **Caching `*big.Int` instances.** sync.Pool of big.Int
  was considered but is more invasive; fast-path skip is
  cheaper.
- **Reordering operands.** `numericCmp(a, b)` semantics
  preserved; we just add a fast-path before the existing
  body.
- **Changing public Datum API.** `NumericMantissaValue()`,
  `NumericBigValue()`, `Scale` accessors unchanged.

## Proposed implementation

### `numericCmp` (numeric.go:416-425)

```go
func numericCmp(a, b Datum) (int, error) {
    // Fast path: both operands in int64 lane.
    if a.NumericBigValue() == nil && b.NumericBigValue() == nil {
        cmp, ok := numericCmpInt64Fast(
            a.NumericMantissaValue(), a.Scale,
            b.NumericMantissaValue(), b.Scale,
        )
        if ok {
            return cmp, nil
        }
        // Fall through to big.Int slow path on overflow.
    }
    // Existing big.Int path.
    am, bm, _ := alignNumericBig(numericMant(a), a.Scale,
                                  numericMant(b), b.Scale)
    switch am.Cmp(bm) {
    case -1: return -1, nil
    case 1:  return 1, nil
    }
    return 0, nil
}

func numericCmpInt64Fast(am int64, ascale int16,
                          bm int64, bscale int16) (int, bool) {
    am2, bm2, ok := alignNumericInt64(am, ascale, bm, bscale)
    if !ok {
        return 0, false
    }
    switch {
    case am2 < bm2: return -1, true
    case am2 > bm2: return 1, true
    }
    return 0, true
}

func alignNumericInt64(am int64, ascale int16,
                        bm int64, bscale int16) (int64, int64, bool) {
    diff := int(ascale) - int(bscale)
    switch {
    case diff == 0:
        return am, bm, true
    case diff > 0:
        // a has more decimals; scale b up by 10^diff
        scaled, ok := mulInt64Pow10(bm, diff)
        if !ok {
            return 0, 0, false
        }
        return am, scaled, true
    case diff < 0:
        scaled, ok := mulInt64Pow10(am, -diff)
        if !ok {
            return 0, 0, false
        }
        return scaled, bm, true
    }
    return 0, 0, false
}

func mulInt64Pow10(v int64, exp int) (int64, bool) {
    if exp == 0 {
        return v, true
    }
    // 10^18 < MaxInt64 < 10^19. Beyond exp=18 always overflows
    // unless v == 0.
    if exp > 18 {
        return 0, v == 0
    }
    p := int64Pow10[exp]
    // Bounds check via division: result must satisfy
    // |v * p| <= MaxInt64. Equivalent: v in [MinInt64/p, MaxInt64/p].
    if v > 0 {
        if v > math.MaxInt64/p {
            return 0, false
        }
    } else if v < 0 {
        // MinInt64/p rounds toward zero in Go; for negative v,
        // the safe lower bound is also MinInt64/p (when p > 0).
        if v < math.MinInt64/p {
            return 0, false
        }
    }
    return v * p, true
}

var int64Pow10 = [19]int64{
    1, 10, 100, 1000, 10_000, 100_000, 1_000_000,
    10_000_000, 100_000_000, 1_000_000_000,
    10_000_000_000, 100_000_000_000, 1_000_000_000_000,
    10_000_000_000_000, 100_000_000_000_000,
    1_000_000_000_000_000, 10_000_000_000_000_000,
    100_000_000_000_000_000, 1_000_000_000_000_000_000,
}
```

### `numericAdd` / `numericSub`

Same wrapping pattern: align scales via `alignNumericInt64`;
if `ok`, compute `am2 + bm2` (or `-bm2`) with overflow
check via `math/bits.Add64Overflow`-style helper. Result
scale = max(ascale, bscale). On overflow → fall through
to existing big.Int path.

### `numericMul`

Compute `am * bm` with overflow check; result scale =
ascale + bscale. Fall through on overflow.

### `numericDiv`

Most delicate: division must preserve `numericMinSigDigits`
significant digits (matching upstream Postgres byte-for-byte).
For int64 fast-path: only apply when ascale + numericMinSigDigits
fits in int64 multiplied. Otherwise fall through.

## Verification

Pre-commit gate (M0074 standard):
- `go test ./internal/executor/... -run Numeric` PASS.
- 21-query SF=1 sweep: zero row-count change. Numerics
  must round-trip identically — fast path and slow path
  produce the same result.
- Q1 / Q3 / Q5 / Q9 spot pprof: `numericMant` flat % ≤
  1 % (was inside `compareDatum`'s 5.86 % flat).

New tests in `internal/executor/numeric_int64_fast_test.go`:
- `TestNumericCmpInt64FastBasic` — int64 mantissas at
  scales (0, 2, 6, 15) for equal/less/greater.
- `TestNumericCmpInt64FastOverflowFallback` — values
  near MaxInt64 with scale diff > 18 force fallback;
  result matches big.Int slow path.
- `TestNumericArithInt64FastNegativeOperands` — sign
  handling on add/sub/mul/div with negative mantissas.
- `TestNumericArithInt64FastVsBigPath` — randomized
  1000-pair fuzz: int64 fast-path output must match
  big.Int slow-path exactly.
- `TestMulInt64Pow10Overflow` — boundary at MaxInt64 /
  pow10[exp].

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | Overflow detection bug in `mulInt64Pow10` → silent wraparound → wrong arithmetic | Use division-based check (`v > MaxInt64/p`) over multiplication; fuzz vs big.Int. |
| R2 | Sign-handling diverges from upstream Postgres on negative scale-aligned values | Pin against existing big.Int semantics in fuzz; sign-extend before divmod. Go's `int64 / int64` rounds toward zero; must match upstream's floor behaviour for negative inputs. |
| R3 | Tests pass but production hits an unexercised path | Run full SF=1 sweep + Q1/Q3 result-byte-equality check vs M0073-final dataset. |

## Migration plan

Single commit (Commit B in M0074):
1. Land helpers (`numericCmpInt64Fast`,
   `alignNumericInt64`, `mulInt64Pow10`, `int64Pow10`).
2. Wrap `numericCmp`, `numericAdd`, `numericSub`,
   `numericMul`, `numericDiv` with fast-path arms.
3. Land tests.
4. Verify gate: SF=1 row count parity + Q5 pprof spot.

If ANY query loses or gains rows → revert immediately.
The fast path must be result-equivalent to the slow path.
