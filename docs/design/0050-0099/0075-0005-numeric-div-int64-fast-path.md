# Design 0075-0005 — numericDiv int64 fast-path

**Milestone:** M0075-0005
**Status:** draft
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** M0074-0006 (commit `8080efa`) —
`mulInt64Pow10`, `alignNumericInt64`, `int64Pow10`.

## Context

M0074-0006 wrapped `numericCmp / numericAdd / numericSub /
numericMul` with int64 fast-paths but deferred
`numericDiv` because its result-scale calculation
(`numericDivScale`, `numeric.go:354-377`) uses upstream
PostgreSQL's NBASE=10000 formula via decimal-string
conversion (`nbaseWeightAndFirstDigit`).

Research (Explore agent, 2026-05-10) found the int64
fast-path is tractable:
- Both operands `Big == nil` (int64 lane).
- Shifted numerator `am * 10^shift` (where
  `shift = rscale + db - da`) must fit in int64.
- Use `mulInt64Pow10` (M0074-0006) for the shift step
  with built-in overflow detection.
- Byte-for-byte rounding match with the big.Int path is
  achievable using int64 quotient + half-away-from-zero
  with explicit sign handling for the `q == 0` edge case.

Coverage: `numericDiv` is called in `avg()` aggregation
when the sum is `KindNumeric`. TPC-H Q1 / Q3 / Q5 / Q14
all hit it. Aggregate path: per-group, not per-row, so
the per-call overhead reduction (~1 µs big.Int alloc →
~50 ns int64 arith) is the main win.

## Goals

- Add int64 fast-path arm to `numericDiv` that skips
  `big.Int` allocation when both operands are int64-lane
  and the shifted numerator fits in int64.
- New helper `numericDivInt64Fast(a, b Datum) (Datum,
  bool)` — returns `(result, ok)`; `ok=false` falls
  through to existing big.Int path.
- New helper `numericDivScaleInt64(am, bm int64, da, db
  int) int` — int64 mirror of `numericDivScale`. Compute
  weights via `bits.Len64` instead of decimal-string
  conversion (faster + no allocation).
- Result-correctness: int64 fast-path output must match
  big.Int slow-path bit-for-bit on every input pair where
  the fast path returns `ok=true`.

## Non-goals

- **Replacing the big.Int slow path.** Big mantissas
  (NUMERIC precision > 18 digits) still go through it.
- **Changing the public `numericDiv` API.**
- **Changing rounding semantics** beyond what the
  big.Int path already does.

## Proposed implementation

### `numericDiv` wrapper

```go
func numericDiv(a, b Datum, pos int) (Datum, error) {
    if a.NumericBigValue() == nil && b.NumericBigValue() == nil {
        bm := b.NumericMantissaValue()
        if bm == 0 {
            return Datum{}, &ExecError{
                Code: "22012", Pos: pos,
                Message: "division by zero",
            }
        }
        if d, ok := numericDivInt64Fast(a, b); ok {
            return d, nil
        }
    }
    // Existing big.Int slow path (preserved verbatim).
    bmBig := numericMant(b)
    if bmBig.Sign() == 0 {
        return Datum{}, &ExecError{Code: "22012", Pos: pos, Message: "division by zero"}
    }
    am := numericMant(a)
    da, db := int(a.Scale), int(b.Scale)
    if am.Sign() == 0 {
        // ... existing zero-numerator path ...
    }
    rscale := numericDivScale(am, bmBig, da, db)
    // ... existing shift + integer divide + round ...
}
```

### `numericDivInt64Fast` body

```go
func numericDivInt64Fast(a, b Datum) (Datum, bool) {
    am := a.NumericMantissaValue()
    bm := b.NumericMantissaValue()
    da, db := int(a.Scale), int(b.Scale)
    
    // Zero numerator → result scale = max(da, db, 0).
    if am == 0 {
        zScale := da
        if db > zScale { zScale = db }
        if zScale < 0 { zScale = 0 }
        return Datum{Kind: KindNumeric, Int: 0, Scale: int16(zScale)}, true
    }
    
    rscale := numericDivScaleInt64(am, bm, da, db)
    shift := rscale + db - da
    
    // Numerator scaling: am * 10^shift (or am / 10^|shift| for negative shift).
    var num int64
    if shift > 0 {
        v, ok := mulInt64Pow10(am, shift)
        if !ok { return Datum{}, false }
        num = v
    } else if shift < 0 {
        s := -shift
        if s > 18 { return Datum{}, false }
        num = am / int64Pow10[s]
    } else {
        num = am
    }
    
    // Integer divide.
    q := num / bm
    rem := num % bm
    
    // Round half-away-from-zero (matches big.Int slow path).
    absRem := rem
    if absRem < 0 { absRem = -absRem }
    absB := bm
    if absB < 0 { absB = -absB }
    
    if 2*absRem >= absB {
        if q == 0 {
            // Sign of unrepresentable quotient comes from num*bm.
            if (num < 0) != (bm < 0) {
                q = -1
            } else {
                q = 1
            }
        } else if q > 0 {
            q++
        } else {
            q--
        }
    }
    
    return Datum{Kind: KindNumeric, Int: q, Scale: int16(rscale)}, true
}
```

### `numericDivScaleInt64` body

```go
func numericDivScaleInt64(am, bm int64, da, db int) int {
    // Mirror upstream's NBASE=10000 weight/firstdigit formula.
    // Decimal digit count of |am| and |bm| via bits.Len64 + log10
    // table; converts to NBASE weight by /4.
    absA := am
    if absA < 0 { absA = -absA }
    absB := bm
    if absB < 0 { absB = -absB }
    
    ndA := decimalDigitCount(absA) // 1..19
    ndB := decimalDigitCount(absB)
    
    // MSB decimal positions (from upstream formula).
    msbA := ndA - 1 - da
    msbB := ndB - 1 - db
    
    // NBASE weights = floor(msb / 4) with floor-div semantics.
    wA := floorDiv4(msbA)
    wB := floorDiv4(msbB)
    
    // First NBASE digit (4 leading decimal digits aligned).
    fdA := firstNBaseDigitInt64(absA, ndA, da, wA)
    fdB := firstNBaseDigitInt64(absB, ndB, db, wB)
    
    qweight := wA - wB
    if fdA <= fdB {
        qweight--
    }
    rscale := numericMinSigDigits - qweight*4
    if da > rscale { rscale = da }
    if db > rscale { rscale = db }
    if rscale < 0 { rscale = 0 }
    if rscale > numericMaxDisplayScale { rscale = numericMaxDisplayScale }
    return rscale
}

// decimalDigitCount returns 1..19.
func decimalDigitCount(v int64) int {
    if v == 0 { return 1 }
    // Lookup table approach
    bitsLen := bits.Len64(uint64(v))
    // log10(2) ≈ 0.30103 → bitsLen * 1233 / 4096 ≈ log10
    // (avoiding floating-point math).
    n := (bitsLen * 1233) >> 12
    if n > 0 && v < int64Pow10[n] {
        n--
    }
    return n + 1
}

// floorDiv4 implements floor division by 4 (Go's / rounds toward zero).
func floorDiv4(x int) int {
    if x >= 0 { return x / 4 }
    return -((-x + 3) / 4)
}

// firstNBaseDigitInt64 — extract the first 4-digit NBASE chunk
// from |v|·10^-dscale aligned to NBASE position w.
func firstNBaseDigitInt64(absV int64, nd, dscale, w int) int {
    // Mirror nbaseWeightAndFirstDigit's logic but on int64 directly.
    msbDecPos := nd - 1 - dscale
    posInNBASE := msbDecPos - 4*w // 0..3
    // First NBASE digit value: top (posInNBASE+1) decimal digits, padded.
    firstdigit := 0
    for i := 0; i <= posInNBASE; i++ {
        var d int
        if i < nd {
            // Extract i-th digit from absV (MSB first).
            divisor := int64Pow10[nd-1-i]
            d = int((absV / divisor) % 10)
        }
        firstdigit = firstdigit*10 + d
    }
    return firstdigit
}
```

## Verification

Pre-commit gate (M0075 standard):
- 21-q SF=1 sweep PASS: zero row-count change. Numerics
  must round-trip identically.
- Q1 / Q3 / Q14 spot pprof: `numericDiv` flat % drops
  vs M0074-final.
- `go test ./internal/executor/... -run NumericDiv` PASS.

New tests in `internal/executor/numeric_div_int64_test.go`:
- `TestNumericDivInt64FastBasic` — TPC-H scales 0/2/6,
  positive / negative sign combinations.
- `TestNumericDivInt64FastVsBigPath` — 1000-pair fuzz;
  int64 fast-path output must match big.Int slow-path
  byte-for-byte (compare via `numericCmp`).
- `TestNumericDivInt64FastZeroNumerator` — short-circuit.
- `TestNumericDivInt64FastDivisionByZero` — surfaces
  22012.
- `TestNumericDivInt64FastShiftOverflowFallthrough` —
  forced fallback when shift > 18.
- `TestNumericDivInt64FastQEqualsZeroSignHandling` —
  the tricky `q == 0` rounding sign edge case.
- `TestDecimalDigitCountBoundary` — pin
  `decimalDigitCount` at all 10^k boundaries.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | Sign-handling bug at `q == 0` (sign comes from `num*bm`) → silent wrong rounding | Dedicated unit test pinning all four sign combinations of `(num, bm)` at the round-up boundary; fuzz vs big.Int. |
| R2 | `decimalDigitCount` boundary inconsistency at 10^k transitions | Lookup-table approach + boundary unit test; fuzz vs slow path catches any divergence. |
| R3 | `floorDiv4` divergence from upstream's negative-weight handling | Pin against existing big.Int `numericDivScale` semantics in fuzz. |
| R4 | Result-scale calculation drifts from upstream Postgres byte-for-byte at edge values | Direct fuzz comparison against the big.Int path's `numericDivScale`. |

## Migration plan

Single commit (Commit B in M0075):
1. Land helpers (`numericDivInt64Fast`,
   `numericDivScaleInt64`, `decimalDigitCount`,
   `floorDiv4`, `firstNBaseDigitInt64`).
2. Wrap `numericDiv` with fast-path branch.
3. Land tests.
4. Verify gate: SF=1 row-count parity + Q1/Q3 spot pprof.

If ANY query loses or gains rows → revert immediately.
The fast path must be result-equivalent to the slow path.
