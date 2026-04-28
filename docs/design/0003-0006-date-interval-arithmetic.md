# Date / Interval Arithmetic (Milestone 0003)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-28                                             |
| Milestone  | 0003 — HammerDB TPC-H Workload                         |
| Refines    | [root-0010-parser.md](root-0010-parser.md), [root-0012-executor.md](root-0012-executor.md) |
| Supersedes | —                                                      |

## Problem

8 of 22 TPC-H queries gate a `WHERE` clause on a date range
expressed as `<column> <op> date 'YYYY-MM-DD' ± interval 'N'
unit`:

- Q1: `l_shipdate <= date '1998-12-01' - interval '90' day`
- Q4: `o_orderdate < date '1993-07-01' + interval '3' month`
- Q5: `o_orderdate < date '1994-01-01' + interval '1' year`
- Q6: `l_shipdate < date '1994-01-01' + interval '1' year`
- Q12: same year shape
- Q14: `l_shipdate < date '1995-09-01' + interval '1' month`
- Q15: same 3-month shape
- Q20: same year shape

Without typed-literal date / interval syntax and the
`time ± interval` arithmetic, none of these queries parse.

## Upstream reference

- `postgres/src/backend/parser/gram.y` — `ConstTypename`
  ConstValue (typed-string-literal grammar) and the
  `INTERVAL` interval-qualifier productions.
- `postgres/src/backend/utils/adt/datetime.c` /
  `timestamp.c` — date/timestamp parsing and the
  `timestamp_pl_interval` family.
- `postgres/src/include/datatype/timestamp.h` — interval
  representation: months / days / microseconds.

## Decisions

### Typed string literals are first-class AST nodes

`date 'YYYY-MM-DD'`, `timestamp 'YYYY-MM-DD HH:MM:SS'`, and
`timestamptz '...'` parse as a new `parser.TypedStringLit`
node with the lower-cased type name + raw string value. The
parser detects the shape at primary-expression time:

```
TokenIdent (lowercase: "date" / "timestamp" / "timestamptz")
  followed-by TokenStringLit
```

The `tryTypedLiteral` helper peeks one token ahead; if the
match fails, the parser position is unchanged and the normal
column-or-call path runs. This avoids reserving `date`,
`timestamp`, `timestamptz` as global keywords (they're column
type names that double as literal-prefix sugar).

### Interval literal: integer + single unit

`interval 'N' unit` is the only interval form we accept in v0.
Recognised units are day(s), month(s), year(s); plurals
normalise to singular. Sub-day units (hour/minute/second),
multi-field intervals (`'1 day 5 hours'`), and negative
literals are deferred. v0 covers what HammerDB's TPC-H actually
uses.

`parser.IntervalLit{Value, Unit}` carries the parsed parts
verbatim; the executor parses `Value` to int32 at evaluation.

### Datum: KindInterval with months + days fields

`Datum.IntervalMonths` and `Datum.IntervalDays` (both int32)
hold the v0 interval representation. Year intervals normalise
to months at parse time (`interval '1' year` → 12 months);
day intervals stay separate. Sub-day microseconds are absent
— no upstream code path that consumes intervals in v0 needs
them.

### `time ± interval` in evalBinary

The binary-op evaluator's `+` / `-` cases route through
`addTimeInterval(t, iv, subtract)` whenever the operand
kinds are `(KindTime, KindInterval)` (or `(KindInterval,
KindTime)` for `+`). The implementation uses Go's
`time.AddDate(years, months, days)` so month-end overflow
behaves like upstream PG (e.g. `2023-01-31 + 1 month` →
`2023-03-03`, matching upstream's "calendar arithmetic"
semantics rather than "30-day months").

`timestamp - timestamp` (returns interval upstream) is NOT
supported in v0 — none of the TPC-H queries that load-bearing
shapes use it.

### Analyzer: timestamp/date are interchangeable

`isTimestampLike` covers all three of `timestamp`,
`timestamptz`, `date`. The analyzer permits comparison
between any pair of timestamp-likes (Q1's
`l_shipdate <= date '...'` shape) and treats
`timestamp ± interval` as resulting in `timestamp`.

## Verification

End-to-end against `goopg start -D <dir>` with upstream psql
18.3:

```
CREATE TABLE lineitem (l_shipdate TIMESTAMP, …);
INSERT … to_timestamp('1998-08-15','YYYY-MM-DD') …  -- 4 rows

-- Q1 shape
SELECT l_shipdate FROM lineitem
  WHERE l_shipdate <= date '1998-12-01' - interval '90' day;
-- only 1998-08-15 row

-- Q4/Q5/Q6 range shape
SELECT l_shipdate FROM lineitem
  WHERE l_shipdate >= date '1998-09-01'
    AND l_shipdate <  date '1998-09-01' + interval '2' month;
-- 1998-09-15, 1998-10-15

-- Year arithmetic
SELECT date '1995-01-01' + interval '1' year AS one_year_later;
```

All three return the expected rows.

### EXTRACT(field FROM source)

`EXTRACT` has its own grammar — `EXTRACT(field FROM expr)` —
because the field name is in keyword position rather than a
value expression. The parser detects the leading `extract(`
inside `parseColumnOrCall` and dispatches to
`parseExtractExpr`, which consumes `(`, the field ident, the
`FROM` keyword, the source expression, and `)`. The result
is a `parser.ExtractExpr{Field, Source}` AST node.

Supported fields (executor `evalExtract`):

| Field    | Result                          |
| -------- | ------------------------------- |
| year     | calendar year                   |
| month    | 1-12                            |
| day      | 1-31                            |
| hour     | 0-23                            |
| minute   | 0-59                            |
| second   | 0-59 (sub-second deferred)      |
| dow      | 0=Sun … 6=Sat (matches upstream) |
| doy      | 1-366                           |
| epoch    | Unix seconds                    |
| quarter  | 1-4 (computed from month)       |

All fields return `int8`; fractional seconds wait on the type
system. EXTRACT(microsecond FROM …) and EXTRACT(timezone FROM
…) are deferred.

End-to-end with TPC-H Q7's filter shape:

```
SELECT o_orderdate FROM orders
  WHERE EXTRACT(year FROM o_orderdate) = 1995;
-- correctly returns only the 1995-* rows
```

## Out of scope (deferred to subsequent loops)

- Fractional-second EXTRACT (microsecond, millisecond).
- `timestamp - timestamp → interval`.
- Sub-day intervals and multi-field literals.
- `BETWEEN x AND y` (sugar over `>= AND <=`; not strictly
  needed but commonly used).
- `interval '1 month' + interval '1 day'` (interval +
  interval).

## Cross-references

- TPC-H query bodies: HammerDB upstream
  `tpc-h/queries-93-orig.sql`.
- Parser expression AST: [root-0010-parser.md](root-0010-parser.md).
- Executor evaluator: [root-0012-executor.md](root-0012-executor.md).
- Earlier M0003 doc on NUMERIC/CASE plumbing:
  [0003-0004-hammerdb-tpch-integration.md](0003-0004-hammerdb-tpch-integration.md),
  [0003-0005-case-expressions.md](0003-0005-case-expressions.md).
