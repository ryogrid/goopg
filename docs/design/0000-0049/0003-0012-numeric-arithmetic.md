# NUMERIC Arithmetic (Milestone 0003)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-29                                             |
| Milestone  | 0003 — HammerDB TPC-H Workload                         |
| Refines    | [0003-0001-planner-overview.md](0003-0001-planner-overview.md), [0003-0004-hammerdb-tpch-integration.md](0003-0004-hammerdb-tpch-integration.md) |
| Supersedes | —                                                      |

## Problem

Until this loop, NUMERIC values flowed through goopg as `KindString`
— the codec wrote and read decimal text verbatim, but every
arithmetic site rejected non-int operands. TPC-H Q1's central
expression `sum(l_extendedprice * (1 - l_discount))` couldn't
execute: every row's multiply raised "operator * requires integer
operands". Q5/Q7/Q8/Q9/Q10 share the same pattern, and
`avg(l_extendedprice)` would have returned an integer truncation
even if the multiply had worked.

The goal of this loop is to land enough NUMERIC arithmetic that
the TPC-H aggregate-with-arithmetic shapes evaluate correctly,
without committing to a full arbitrary-precision implementation.

## Upstream reference

- `postgres/src/backend/utils/adt/numeric.c` — full
  `NumericVar`/`NumericDigit` implementation.
- `postgres/src/include/utils/numeric.h` — wire format and
  scale/typmod handling.

## Decisions

### Carrier: int64 mantissa + int8 scale

`Datum` grows two fields:

```go
NumericMantissa int64
NumericScale    int8
```

The value is `mantissa * 10^-scale`. `123.45` is `(12345, 2)`,
`-1500` is `(-1500, 0)`, `0.05` is `(5, 2)`. Sign lives in the
mantissa; scale is non-negative.

> **Update (M0041-0004, 2026-05-04)**: this document predates the
> NUMERIC precision fix. The "int64-only" model below is superseded
> — `Datum` carries an optional `NumericBig *big.Int` overflow lane
> for division results that don't fit int64 (TPC-H Q8 needs
> mantissa = 10^20), and `numericDiv` matches upstream's
> `select_div_scale` rule with half-away-from-zero rounding. The
> per-row hot path still uses int64 when values fit, so the
> "avoid big.Int allocation" trade-off below is preserved for
> scans/joins. See `docs/design/0041-0004-numeric-precision-fix.md`.

Why int64 (fast path) and not big.Int (always) / a homegrown digit array:

- TPC-H SF1 magnitudes fit comfortably. Worst-case Q1 accumulator
  is `~6M rows × $999,999.99 × (1 - 0) × (1 + 0.08)` with two
  fractional digits, ≈ `6.5 × 10^15` — three orders of magnitude
  below int64 max.
- Avoids a `*big.Int` per row's allocation cost, which would
  dominate query cost at SF1.
- Arbitrary-precision support is documented as out of scope and
  picked up by the type-system milestone.

The trade-off is overflow: SF10+ Q1 accumulator (`~6.5 × 10^16`)
still fits, but multiplied scales beyond ~17 fractional digits
overflow on the scale-alignment path. Detection is byte-level
(`abs(mantissa) > maxInt64Div10`) and surfaces as `numeric overflow`
rather than silent corruption.

### Parser/Lexer

No changes — `NumericConst` was already a parser/planner node;
this loop only changes its executor mapping from `KindString` to
`KindNumeric`.

### Codec

`encodeValue` for `numeric`/`decimal`:

- `KindNumeric`: format via `formatNumeric(mantissa, scale)` and
  encode in the varlen frame. Trailing-zero behaviour matches
  upstream — `(12345, 4)` writes `1.2345`, `(0, 3)` writes `0.000`.
- `KindInt`, `KindString`, `KindBytes`: round-trip unchanged so the
  existing INSERT paths (HammerDB's quoted-string loader, integer
  literals) keep working without mid-stream coercion.

`decodeValue` parses the varlen text into `KindNumeric` so
arithmetic and comparison can run through the scale-aligning
helpers without re-parsing on every row. Malformed input surfaces
as 22P02.

### Arithmetic

`evalBinary` adds a NUMERIC arm before the integer fallthrough:

- If either operand is `KindNumeric`, both promote to `KindNumeric`
  (`KindInt` → `(int, scale=0)`).
- `+`/`-` use `numericAdd` / `numericSub`: align scales, add/sub
  mantissas at the larger scale.
- `*` uses `numericMul`: multiply mantissas, sum scales. Overflow
  is detected via `canMulInt64` (multiply-then-divide check).
- `/` uses `numericDiv`: target scale is `max(left.scale,
  right.scale, 6)` — matches upstream's NUMERIC division for
  unscaled inputs and is enough for `1 - l_discount` shapes.
  Division by zero raises 22012.
- `%` is not supported in this loop — TPC-H doesn't need it.

### Comparison

`compareDatum` handles `NUMERIC ↔ NUMERIC` and `NUMERIC ↔ INT`
by promoting the int side to scale=0 and aligning. The sort/order
path inherits this for free since it uses `compareDatum`.

### Aggregates

`aggRuntime` grows a `numericSum Datum` field. In `applyAgg`:

- `sum`/`avg` over `KindInt` keeps the existing int64 accumulator.
- `sum`/`avg` over `KindNumeric` uses the new accumulator,
  initialised at the first non-NULL row's scale.
- `min`/`max`/`count` are kind-agnostic — they reuse `compareDatum`
  / row counting unchanged.

`finishAgg` returns `KindNumeric` when the accumulator is NUMERIC,
falling back to int otherwise. `avg` over NUMERIC goes through
`numericDiv` so the result has the post-division scale (≥ 6).

## Verification

End-to-end against `goopg start -D <dir>` with upstream psql 18.3:

```sql
CREATE TABLE lineitem (
  l_orderkey int4, l_extendedprice numeric,
  l_discount numeric, l_tax numeric, l_returnflag text);
INSERT INTO lineitem VALUES
  (1, 100.00, 0.05, 0.08, 'A'),
  (2, 250.50, 0.10, 0.07, 'A'),
  (3, 1000.00, 0.00, 0.05, 'N'),
  (4, 50.25, 0.20, 0.06, 'N');

-- Q1 shape: scalar arithmetic on numeric.
SELECT l_extendedprice, l_discount,
       l_extendedprice * (1 - l_discount) AS disc_price
FROM lineitem ORDER BY l_orderkey;
-- 95.0000, 225.4500, 1000.0000, 40.2000

-- Aggregate over numeric, grouped.
SELECT l_returnflag, sum(l_extendedprice),
       sum(l_extendedprice * (1 - l_discount)),
       avg(l_extendedprice), count(*)
FROM lineitem GROUP BY l_returnflag ORDER BY l_returnflag;
-- A: 350.50, 320.4500, 175.250000, 2
-- N: 1050.25, 1040.2000, 525.125000, 2

-- NUMERIC vs INT comparison.
SELECT l_orderkey FROM lineitem WHERE l_extendedprice > 100;
-- 2, 3

-- NUMERIC vs NUMERIC comparison.
SELECT l_orderkey FROM lineitem WHERE l_discount >= 0.10;
-- 2, 4
```

`TestEncodeDecodeNumericRoundTrip` pins the codec round-trip for
KindInt, KindString, and KindNumeric inputs.

## Out of scope (deferred)

- Arbitrary-precision NUMERIC (big.Int / `numeric_t`-shaped digit
  array). v0 is bounded by int64.
- `numeric(precision, scale)` typmod enforcement — the catalog
  records the args but writes don't reject overflow.
- `%` (modulo), `^` (power), and the rest of upstream's NUMERIC
  operator family.
- NUMERIC ↔ FLOAT promotion — there is no FLOAT carrier in v0.
- Wire-format binary NUMERIC (the upstream NBASE-10000 packed
  digit format). v0 stays text-on-the-wire for NUMERIC.
- Hashing: NUMERIC values hash via `datumKey` = `Format()`, which
  is correct for distinct/group-by but expensive vs a binary hash.
