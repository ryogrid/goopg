# 0043-0003 — MHJ Int64 Fast-Path Hash Tables

**Status:** accepted
**Parent milestone:** M0043
**Date:** 2026-05-04

## 1. Problem

`datumKey(d Datum) string` is called on every probe lookup in the
MultiHashJoin operator. For Q9 at SF=1, ~22 M calls are made during
the 6-table chain probe (6M lineitem rows × 5 steps, minus early-exit
savings from predicate pushdown).

The dominant case is int/numeric-scale-0 join keys (all of Q9's join
columns are `NUMERIC` with integer values). Each call executes:

```
canonicalNumericKey(mantissa, 0)
→ fmt.Sprintf("m:%d:%d", mantissa, 0)
→ ~16-byte heap allocation
```

22M × 16 bytes = **352 MB of short-lived allocations per query**,
causing heavy GC stop-the-world pauses (91% GC overhead observed in
run-005 before M0043-0001).

## 2. Solution: int64 fast-path hash table

If ALL build rows for a given table have int64-representable keys (KindInt
or KindNumeric with scale 0 after trailing-zero normalisation), store them
in a `map[int64][]Row` rather than `map[string][]Row`. During the probe, use
a direct `int64` map lookup — **zero allocation**.

### 2.1 Key converter: `datumToInt64Key`

```go
func datumToInt64Key(d Datum) (int64, bool) {
    switch d.Kind {
    case KindInt:
        return d.Int, true
    case KindNumeric:
        if d.NumericBig != nil { return 0, false }
        m, s := d.NumericMantissa, int(d.NumericScale)
        for s > 0 && m%10 == 0 { m /= 10; s-- }
        if s == 0 { return m, true }
        return 0, false
    }
    return 0, false
}
```

Matches the canonical form of `canonicalNumericKey`: `KindInt(v)` and
`KindNumeric{mantissa=v*10^n, scale=n}` both produce the same int64 key
after normalisation, preserving cross-kind equality semantics.

### 2.2 Build phase (dual-mode)

```go
allInt64 := true
for _, r := range rows {
    if _, ok := datumToInt64Key(r[keyCol]); !ok { allInt64 = false; break }
}
if allInt64 {
    intHt := make(map[int64][]Row, len(rows))
    for _, r := range rows { k, _ := datumToInt64Key(r[keyCol]); intHt[k] = append(intHt[k], r) }
    o.intHashTbls[i] = intHt; o.hashTblIsInt[i] = true
} else {
    ht := make(map[string][]Row, len(rows))
    for _, r := range rows { k := datumKey(r[keyCol]); ht[k] = append(ht[k], r) }
    o.hashTbls[i] = ht
}
```

One-pass scan to decide; if any key fails, fall back to string map.

### 2.3 Probe phase

```go
if o.hashTblIsInt[step.hashTblIndex] {
    if k, ok := datumToInt64Key(keyVal); ok {
        matches = o.intHashTbls[step.hashTblIndex][k]
    }
} else {
    matches = o.hashTbls[step.hashTblIndex][datumKey(keyVal)]
}
```

For Q9's integer join keys: zero allocation per probe step.

## 3. Additional fixes

### 3.1 Double `datumKey()` in build loop

The original code called `datumKey(r[keyCol])` twice per build row:
```go
// BUG: two calls
ht[datumKey(r[keyCol])] = append(ht[datumKey(r[keyCol])], r)
```
The new code caches the key: `k := datumKey(r[keyCol]); ht[k] = append(ht[k], r)`.

### 3.2 `canonicalNumericKey` and `datumKey` KindTime/Interval

Replace `fmt.Sprintf("m:%d:%d", ...)` and `fmt.Sprintf("t:%d", ...)`
with `strconv.AppendInt` into a stack buffer, followed by a single
`string()` conversion. This reduces the per-call overhead for the
string-key fallback path by ~5–10×.

## 4. New struct fields in `multiHashJoinOp`

```go
intHashTbls  []map[int64][]Row // fast-path tables (M0043-0003)
hashTblIsInt []bool            // selector: true → intHashTbls[i] active
```

Both allocated in `Open()` and nilled in `Close()`.

## 5. Correctness properties

- NULL (KindNull) returns `(0, false)` → not stored/found in int map → no
  match. Correct: `NULL = anything` is false in SQL.
- KindBool, KindString, KindTime, KindInterval → `(0, false)` → falls back
  to string map. No regression.
- Build tables with mixed types (some int64, some not) fall back to the
  string map. Correct.
- Cross-kind compatibility (KindInt 5 vs KindNumeric{50, 1}) preserved:
  both normalise to int64(5) in `datumToInt64Key`, or to `"m:5:0"` in
  `canonicalNumericKey`.

## 6. Verification

- `TestDatumToInt64Key` (10 sub-cases): zero/positive/negative int,
  scale-0 numeric, trailing-zeros strip, fractional numeric, null, bool, string.
- `TestMultiHashInt64FastPath`: 3-table int chain; asserts
  `hashTblIsInt[1]=true, hashTblIsInt[2]=true` after Open; verifies 2 joined
  rows with correct cval values.
- `TestTPCHResultParity`: identical=22 divergent=0 errored=0 — no regression.
- `TestRunTPCHQueriesAgainstSyntheticData`: 22/22 PASS.
