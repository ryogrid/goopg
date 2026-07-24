# 06 — Numeric Fast-Path Expansion

| field | value |
| --- | --- |
| priority | LOW-MEDIUM — 4–9% cum alloc across all queries |
| risk | Low |
| files | `internal/executor/numeric.go`, `internal/executor/codec.go` |
| composes with | [03-row-decode-fast-path.md](./03-row-decode-fast-path.md) |

## 1. Motivation

`parseNumeric` via `strings.NewReader` + `math/big.Int.SetString` accounts for
4–9% of cumulative allocation across all TPC-H queries:

| Query | `parseNumeric` cum alloc | % of total alloc |
| --- | ---: | ---: |
| Q1 | 1.72 GB | 5.8% |
| Q9 | 2.31 GB | 5.5% |
| Q4 | 3.08 GB | 4.8% |
| Q7 | 3.84 GB | 4.6% |
| Q13 | 4.30 GB | 4.3% |

`parseNumericFast` already exists (`numeric.go:210`) as an int64 fast path, but
it only handles **integer-only** numerics (scale = 0 after stripping trailing zeros).
All TPC-H numeric columns have non-zero scale (2 or 4), so **`parseNumericFast`
never matches for TPC-H** — it's dead code in the TPC-H hot path.

## 2. Current state

### 2.1 parseNumericFast — integer only (`numeric.go:210-246`)

```go
// parseNumericFast parses a numeric text value when it can be
// represented as an int64. Returns (value, scale, true) on success.
// Only handles plain integers: no decimal point, no exponent.
func parseNumericFast(text string) (int64, int16, bool) {
    // ... strips sign, parses digits ...
    // Returns scale=0 ALWAYS — any fractional part falls through
    // to the slow path.
}
```

For a value like `"123.45"`, this function returns `(0, 0, false)` because
the decimal point triggers the slow-path fallback.

### 2.2 Callers of parseNumericFast

**Primary caller: `decodePhysicalPGValueMctx` numeric case (`codec.go:1199-1206`):**
```go
case "numeric", "decimal":
    payload, n, err := decodePhysicalPGVarlena(data)
    text := string(payload)
    if v, scale, ok := parseNumericFast(text); ok {
        return Datum{Kind: KindNumeric, Int: v, Scale: scale}, n, nil
    }
    m, s, err := parseNumeric(text)   // ← slow path: *big.Int allocation
    return newNumeric(m, int(s)), n, nil
```

**Secondary caller: `floatTextDatum` (`codec.go:1515-1518`)** — the float4/float8
output path also parses its text representation through `parseNumericFast`/`parseNumeric`
to produce a KindNumeric Datum. Same pattern as the decode path.

**Numeric encoding path** (`encodeValuePG` at `codec.go:520`): does NOT currently
use `parseNumericFast` — it rounds-trips through `coerceTextLikeDatum` +
`varlenaTextBytes`. This path could also benefit from the fast path but is lower
priority (encoding is write-path, not scan-path).

### 2.3 The slow path: parseNumeric (`numeric.go:260-314`)

Allocates:
1. `strings.TrimSpace(text)` — new string allocation
2. `strings.ReplaceAll` for underscores — new string if underscores present (rare in TPC-H)
3. Substring slicing for intPart/fracPart — zero-copy
4. `new(big.Int)` at line 297
5. `mantissa.SetString(digits, 10)` — allocates big.Int internal digits
6. Potentially `new(big.Int).Exp(...)` for negative exponents (not used in TPC-H)

For a NUMERIC(15,2) value like `"123.45"`: ~100 bytes of allocation per decoded column.
Multiplied by 9 numeric columns × 6M `lineitem` rows = 54M calls in Q1 alone.

### 2.4 TPC-H numeric columns — all fit in int64

| Column | Type | Scale | Max absolute value | As int64 mantissa |
| --- | --- | ---: | ---: | ---: |
| `l_quantity` | NUMERIC(15,2) | 2 | 50.00 | 5,000 |
| `l_extendedprice` | NUMERIC(15,2) | 2 | ~104,949.50 | ~10,494,950 |
| `l_discount` | NUMERIC(15,4) | 4 | 0.1000 | 1,000 |
| `l_tax` | NUMERIC(15,4) | 4 | 0.0800 | 800 |
| `o_totalprice` | NUMERIC(15,2) | 2 | ~558,821.88 | ~55,882,188 |
| `c_acctbal` | NUMERIC(15,2) | 2 | ~9,999.99 | ~999,999 |
| `s_acctbal` | NUMERIC(15,2) | 2 | ~9,999.99 | ~999,999 |
| `ps_supplycost` | NUMERIC(15,2) | 2 | 1,000.00 | 100,000 |
| `p_retailprice` | NUMERIC(15,2) | 2 | ~2,098.99 | ~209,899 |

Max int64 mantissa: `o_totalprice` at ~55,882,188. MaxInt64 = 9,223,372,036,854,775,807.
Headroom: > 10^11×. **Every TPC-H NUMERIC value fits in int64 as a scaled mantissa.**

## 3. Design

### 3.1 New function: parseNumericFastScale

Add a variant that handles decimal points by parsing the combined digit string
into int64 and recording the fractional digit count as the scale:

```go
// parseNumericFastScale parses a NUMERIC text value with support for
// decimal points. On success, returns the mantissa as int64 and the
// actual scale (number of fractional digits).
//
// The mantissa represents the value × 10^scale. For example:
//   "123.45" → (12345, 2, true)     — 12345 × 10⁻² = 123.45
//   "-0.01"  → (-1, 2, true)        — -1 × 10⁻² = -0.01
//   "42"     → (42, 0, true)        — 42 × 10⁰ = 42
//
// Falls back to (0, 0, false) when:
//   - The value exceeds 18 decimal digits (int64 overflow risk)
//   - An exponent notation (e/E) is present
//   - Any character other than [+-]?[0-9]*[.]?[0-9]* is present
//
// expectedScale, when >= 0, is the column's declared scale
// (e.g., 2 for NUMERIC(15,2)). When expectedScale >= 0 and the actual
// scale differs, the function returns false — the caller should fall
// back to the big.Int path for precise rounding.
func parseNumericFastScale(text string, expectedScale int16) (int64, int16, bool) {
    if len(text) == 0 {
        return 0, 0, false
    }

    // Strip leading sign.
    neg := false
    s := text
    switch s[0] {
    case '+':
        s = s[1:]
    case '-':
        neg = true
        s = s[1:]
    }
    if len(s) == 0 {
        return 0, 0, false
    }

    // Reject exponent notation (not used in TPC-H column data,
    // but valid PG NUMERIC literals can include it).
    if strings.ContainsAny(s, "eE") {
        return 0, 0, false
    }

    // Find decimal point.
    var intPart, fracPart string
    if idx := strings.IndexByte(s, '.'); idx >= 0 {
        intPart = s[:idx]
        fracPart = s[idx+1:]
    } else {
        intPart = s
    }

    digits := intPart + fracPart
    if len(digits) == 0 {
        return 0, 0, false
    }
    if len(digits) > 18 {
        return 0, 0, false // int64 overflow risk
    }

    // Parse digits as int64.
    var v int64
    for i := 0; i < len(digits); i++ {
        c := digits[i]
        if c < '0' || c > '9' {
            return 0, 0, false
        }
        v = v*10 + int64(c-'0')
    }
    if neg {
        v = -v
    }

    actualScale := int16(len(fracPart))

    // When the column has a declared scale, verify it matches.
    // Mismatch means the text representation doesn't match the
    // column's type constraint — fall back to big.Int for precise
    // rounding.
    if expectedScale >= 0 && actualScale != expectedScale {
        return 0, 0, false
    }

    return v, actualScale, true
}
```

### 3.2 Rename existing parseNumericFast

Rename to `parseNumericFastInt` for clarity:

```go
// parseNumericFastInt parses a NUMERIC text value that is an integer
// (no decimal point, no fractional part). Returns (value, scale=0, true)
// on success. Kept for compatibility with callers that expect scale=0
// and for non-column-bounded numeric literals.
func parseNumericFastInt(text string) (int64, int16, bool) {
    // ... existing implementation, unchanged ...
}
```

Keep the old name as a wrapper for backward compatibility (tests, etc.):

```go
// parseNumericFast is the legacy wrapper; prefers integer-only fast path.
// New callers should use parseNumericFastInt or parseNumericFastScale directly.
func parseNumericFast(text string) (int64, int16, bool) {
    return parseNumericFastInt(text)
}
```

### 3.3 Update decode callers

In `decodePhysicalPGValueMctx` numeric case (`codec.go:1190-1206`):

```go
case "numeric", "decimal":
    payload, n, err := decodePhysicalPGVarlena(data)
    if err != nil {
        return Datum{}, 0, err
    }
    text := string(payload)

    // Try integer fast path first (no decimal point).
    if v, scale, ok := parseNumericFastInt(text); ok {
        return Datum{Kind: KindNumeric, Int: v, Scale: scale}, n, nil
    }

    // Try known-scale fast path when the column type declares
    // a precision and scale (all TPC-H NUMERIC columns).
    if t.Scale >= 0 {
        if v, scale, ok := parseNumericFastScale(text, int16(t.Scale)); ok {
            return Datum{Kind: KindNumeric, Int: v, Scale: scale}, n, nil
        }
    }

    // Fall through to big.Int slow path.
    m, s, err := parseNumeric(text)
    if err != nil {
        return Datum{}, 0, fmt.Errorf("decode numeric %q: %w", text, err)
    }
    return newNumeric(m, int(s)), n, nil
```

### 3.4 How the scale is stored

The Datum already has a `Scale int16` field (`datum.go:109`). For int64-fast-path
numerics, the value is `Int × 10^Scale`. The existing numeric arithmetic
functions (`numericAdd`, `numericMul`, etc. in `numeric.go`) already handle
int64-fast-path numerics — they check whether both operands and the result
fit in int64, and fall back to `big.Int` when they don't. No changes needed
in the arithmetic layer.

The existing `Datum{Kind: KindNumeric, Int: 12345, Scale: 2}` encoding means
the value is `12345 × 10⁻² = 123.45`. This is already the convention used by
the existing `parseNumericFast` and the type system.

### 3.5 catalog.Type.Scale field

Verify that `catalog.Type` has a `Scale` field accessible during decode.
From the code exploration:

- `catalog.Type` is used in `decodePhysicalPGValueMctx` as `t catalog.Type`
- The numeric decode case checks `t.Scale` — this field must be populated
  from the column definition
- For `NUMERIC(15,2)`, `t.Scale` should be `2`
- For `NUMERIC` (no precision/scale), `t.Scale` should be `-1` or `0` as a sentinel

If `t.Scale` is not yet populated in the catalog, a prerequisite step is to
ensure `catalog.Column.Type.Scale` reflects the declared scale from
`information_schema.columns.numeric_scale` or the PG catalog.

## 4. Prerequisites

**`catalog.Type.Scale` must be populated** for NUMERIC columns before this fix
can take effect on the decode path. Verify with:

```bash
grep -n "Scale\b" internal/catalog/type.go | head -10
```

If `catalog.Type.Scale` is not populated from the column definition, the
`parseNumericFastScale` fast path becomes dead code for the most impactful
use case (scan decode). Populating it is a blocking prerequisite:

1. Ensure `catalog.Type.Scale` reflects the declared scale from the column type
   (e.g., 2 for `NUMERIC(15,2)`, 4 for `NUMERIC(15,4)`, -1 sentinel for
   unconstrained `NUMERIC`).
2. The PG catalog source is `information_schema.columns.numeric_scale` or the
   `pg_attribute.atttypmod` encoding.

## 5. Implementation steps

1. **Add `parseNumericFastScale` function** in `internal/executor/numeric.go`.
2. **Rename existing `parseNumericFast`** to `parseNumericFastInt`.
3. **Add backward-compatible `parseNumericFast` wrapper** that delegates to
   `parseNumericFastInt`.
4. **Confirm prerequisite** — `catalog.Type.Scale` is populated (see §4).
5. **Update `decodePhysicalPGValueMctx`** numeric case to try
   `parseNumericFastInt` → `parseNumericFastScale` → `parseNumeric` (slow path).
6. **(Optional)** Update `floatTextDatum` (`codec.go:1515`) similarly.
7. **(Optional)** Update `encodeValuePG` numeric encoding (`codec.go:520`) to
   try the fast path first before `coerceTextLikeDatum` → `varlenaTextBytes`.
8. **Add unit tests** covering edge cases.
9. **Benchmark**: Compare alloc_space for Q1 before/after.

## 6. Risk assessment

| Risk | Impact | Mitigation |
| --- | --- | --- |
| Scale mismatch between declared and actual | Wrong numeric value (e.g., "123.4" stored in NUMERIC(15,2) → returns 4 as scale when expected 2) | `parseNumericFastScale` returns false on scale mismatch → falls through to slow path, which parses correctly |
| int64 overflow for edge-case values | Wrong value | Reject > 18 digits in `parseNumericFastScale` → falls through to slow path |
| Exponential notation in table data | Wrong value (e/E rejected by fast path) | Falls through to slow path — correct |
| `catalog.Type.Scale` not populated | Fast path never taken (expectedScale is sentinel) | Falls through to `parseNumericFastInt` (integer-only) or slow path |
| Non-TPC-H NUMERIC columns with larger scale/precision | Fast path rejected (> 18 digits) | Falls through to slow path — correct |

## 7. Verification

1. **Unit tests for `parseNumericFastScale`:**
   ```go
   func TestParseNumericFastScale(t *testing.T) {
       tests := []struct{
           text     string
           expScale int16
           wantV    int64
           wantS    int16
           wantOk   bool
       }{
           {"123.45", 2, 12345, 2, true},
           {"-0.01", 2, -1, 2, true},
           {"0", 0, 0, 0, true},
           {"123.4", 2, 0, 0, false},  // scale mismatch
           {"123.45", -1, 12345, 2, true}, // no expected scale
           {"999999999999999999", 0, 0, 0, false}, // > 18 digits
       }
       for _, tt := range tests {
           v, s, ok := parseNumericFastScale(tt.text, tt.expScale)
           if v != tt.wantV || s != tt.wantS || ok != tt.wantOk {
               t.Errorf("parseNumericFastScale(%q, %d) = (%d, %d, %v), want (%d, %d, %v)",
                   tt.text, tt.expScale, v, s, ok, tt.wantV, tt.wantS, tt.wantOk)
           }
       }
   }
   ```

2. **Decode parity test:** Decode the same NUMERIC values with and without
   the fast path; verify Datum equality:
   ```go
   // Verify that fast-path and slow-path produce identical Datums
   // for all TPC-H NUMERIC values.
   ```

3. **Allocation profile:**
   ```bash
   go tool pprof -sample_index=alloc_space -top bench/tpch/pprof/q1_allocs_after.pb.gz
   ```
   Expected: `parseNumeric` drops from top 10 allocators entirely.
   For Q1, alloc reduction of ~1.72 GB.

4. **Full TPC-H result parity:**
   ```bash
   tmp/tpch-runner --port=65433 --queries=all --parallel-workers=0
   ```
   All numeric output columns must match the baseline (no rounding differences).

## 8. Related improvements

- [03-row-decode-fast-path.md](./03-row-decode-fast-path.md) — the decode strategy that includes `makeNumericColDecode(scale)`, which directly calls `parseNumericFastScale` without the if/else chain. Implement Fix 06 before Fix 03 so the strategy can include the known-scale numeric decode function.
- The int64 fast-path arithmetic (`numericAdd`, `numericMul`, `numericDiv`, `numericCmp` in `numeric.go`) already exists — Fix 06 ensures values land on the int64 lane from the start, avoiding big.Int promotion during decode.
