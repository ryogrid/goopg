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

`interval 'N' unit` (Form 1, trailing-unit) and
`interval '<N> <unit>'` (Form 2, embedded-unit) both produce
the same AST node. HammerDB's TPC-H query templates emit Form 2
— Q1's `interval ':1 day'` becomes `interval '90 day'` after
parameter substitution; Q4 uses `interval '3 month'`; Q5/Q6/Q12
use `interval '1 year'` — so v0 has to accept both shapes for
the upstream queries to parse without source rewrites. The
parser tries Form 1 first (peek-2 ident); on miss it splits the
string-literal body via `splitEmbeddedInterval` and falls back
to Form 2.

Recognised units are day(s), month(s), year(s); plurals
normalise to singular. Sub-day units (hour/minute/second),
multi-field intervals (`'1 day 5 hours'`), and the SQL-standard
`INTERVAL [+|-] '<N>' YEAR TO MONTH`-style qualifiers are
deferred. v0 covers what HammerDB's TPC-H actually uses.

`parser.IntervalLit{Value, Unit}` carries the parsed parts
verbatim; the executor parses `Value` to int32 at evaluation.
Negative magnitudes parse fine — `splitEmbeddedInterval`
preserves the leading `-` so `interval '-1 day'` round-trips.

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

## Follow-up: interval ordering comparisons (M0122-0004, 2026-07-06)

`compareDatum` (`internal/executor/expr.go`) had cases for every `Datum.Kind`
used in `<`/`>`/`<=`/`>=`/`ORDER BY`/`MIN`/`MAX` **except `KindInterval`** —
any of those over an interval value raised `42883` ("comparison not
supported for kind ..."), even though interval *equality* already worked
correctly via `datumKey` (`internal/executor/operators_join_agg.go`, used by
GROUP BY/DISTINCT hashing). Confirmed as a genuine gap by grepping
`unimplemented_feat.json` for `interval` and cross-checking against a real
PostgreSQL 18.3 instance.

Fix mirrors upstream's `interval_cmp_value` (`postgres/src/backend/utils/
adt/timestamp.c`): months are widened to days at a fixed 30-day rate, added
to the day field, and the results linearized as a single `int64` day count
before comparing (`at := months*30 + days`). Upstream widens further to
microseconds via the interval's `time` field; v0's interval has no sub-day
component (always 0), so that extra step is a no-op here and the two
representations agree exactly. Verified against real PostgreSQL 18.3
byte-for-byte, including the tie case (`interval '3' month = interval '90'
day` — both linearize to 90) and negative months.

Tests: `internal/executor/interval_compare_test.go`
(`TestIntervalOrderingOperators` — 6 comparison-operator cases;
`TestIntervalOrderByAndMinMax` — `ORDER BY`/`MIN`/`MAX` over
interval-valued expressions).

Deferred (ledger row, unchanged from this doc's original "Out of scope"
list): sub-day interval units, `CAST(... AS interval)` string parsing, and
`Datum.Format()`'s `KindInterval` text rendering (`"%d months %d days"`,
which does not match PostgreSQL's `intervalout` — e.g. real PG prints `"3
mons"` not `"3 months 0 days"`) all remain open, independent gaps.

## Follow-up: `Datum.Format()` interval text rendering (M0122-0004, 2026-07-06)

Closes the `Datum.Format()`/`intervalout` gap the prior follow-up named.
`formatInterval(months, days int32) string` (`internal/executor/datum.go`)
replaces the old unconditional `"%d months %d days"` with upstream's actual
`interval_out` shape under the default `'postgres'` `IntervalStyle`, verified
live against a real PostgreSQL 18.3 instance
(`postgres/local_install/bin/psql`) rather than derived from the C source:

- Total months split into `years := months/12`, `remMonths := months%12`
  (Go's truncating integer division/modulo already matches C's, so this is a
  direct port — no special-casing for negative months needed).
- Each of years/remMonths/days is rendered only if nonzero, as `"<n>
  <unit>"` with the *plural* unit text unless the value is *exactly* `1`
  (empirically, `-1` still takes the plural form, e.g. `"-1 mons"` not `"-1
  mon"` — confirmed against real PG, not assumed).
- Components are space-joined in years → months → days order; a
  fully-zero interval (0 months, 0 days) prints PG's special-cased
  `"00:00:00"` rather than an empty string.

Verified cases (each cross-checked against real PG 18.3): `14,3 → "1 year 2
mons 3 days"`; `-1,0 → "-1 mons"`; `0,0 → "00:00:00"`; `13,0 → "1 year 1
mon"` (total-months normalization, not a pre-split year/month field);
`-15,0 → "-1 years -3 mons"`; `-12,0 → "-1 years"` (zero remainder omitted);
`11,0 → "11 mons"` and `-11,0 → "-11 mons"` (zero years omitted). Test:
`internal/executor/interval_format_test.go`
(`TestFormatIntervalMatchesPGIntervalOut`, 18 cases).

Still deferred, unchanged: sub-day interval units and `CAST(... AS
interval)` string parsing (this v0 interval type has no time-of-day
component to parse into or format, so PG's `HH:MM:SS` suffix never
appears except via the zero-interval special case above).

## Follow-up: `CAST(... AS interval)` string parsing (M0122-0004, 2026-07-06)

Closes the `CAST(... AS interval)` string-parsing gap the two follow-ups
above both listed as still open. `evalCast` (`internal/executor/expr.go`) had
no `"interval"` case at all, so `'3 days'::interval` / `CAST('3 days' AS
interval)` fell to the function's final `return d, nil` ("pass-through for
unknown types") — the value silently stayed a `KindString` holding the raw
text instead of becoming a real `KindInterval`, so e.g. `now() +
'3 days'::interval` would misbehave (adding a string to a timestamp, not an
interval) instead of computing correctly or erroring cleanly.

The new `"interval"` case accepts only the same "`<n> <unit>`" shape the
`INTERVAL '<n> <unit>'` typed-literal grammar already supports (unit ∈
day(s)/month(s)/year(s), case-insensitive) — deliberately not the full
PostgreSQL `interval_in` grammar (multi-component strings, sub-day
`HH:MM:SS`, ISO-8601 durations), which remains the documented "sub-day
intervals" v0 scope limit this doc's "Out of scope" section already names.
New `parseIntervalCastString(s string) (months, days int32, ok bool)`
(`internal/executor/expr.go`, next to `evalIntervalLit`) mirrors
`splitEmbeddedInterval`'s two-token split + `evalIntervalLit`'s
unit-to-months/days mapping so the cast path and the typed-literal path
accept exactly the same strings. An unparseable string raises `22007`
("invalid input syntax for type interval"), matching real PostgreSQL's
`interval_in` SQLSTATE for the same failure mode (verified: real PG raises
`22007` for a garbage interval string too, even though v0's *accepted*
grammar is narrower than upstream's).

Tests: `internal/executor/interval_cast_test.go`
(`TestIntervalCastFromString` — `::interval`/`CAST(...)` forms across
day/month/year, singular/plural units, negative magnitudes, cross-checked
against the equivalent `INTERVAL '<n>' <unit>` typed literal via
`compareDatum`'s existing interval-ordering support;
`TestIntervalCastFromStringInvalidSyntax` — garbage, unsupported unit, and
two shapes real PG accepts but v0 deliberately doesn't yet
(`'1 year 2 months'`, `'01:02:03'`) all raise `22007`). Gates: `go build
./...` clean; `go test ./internal/executor/... ./internal/planner/...
./internal/parser/... ./internal/analyzer/...` PASS (no regressions);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).

Still deferred, unchanged: sub-day interval units and multi-component
interval strings (`'1 year 2 months 3 days'`, `'01:02:03'`) — both the typed
literal and the cast path reject them identically now, so there is no
remaining asymmetry between the two entry points for this v0 interval type.

## Follow-up: `justify_hours`/`justify_days`/`justify_interval` (M0097-0004, 2026-07-07)

`evalJustifyInterval` (`internal/executor/expr.go`) was a stub that returned
its argument unchanged for all three functions — `unimplemented_feat.json`'s
`m0097-0004` entry. Implemented for real: `justify_days()`/`justify_interval()`
now move whole 30-day chunks out of the day field into the month field, then
equalize the sign of both fields, mirroring upstream's
`interval_justify_days`/`interval_justify_interval`
(`postgres/src/backend/utils/adt/timestamp.c`) exactly. Upstream's
`justify_interval` additionally folds in `interval_justify_hours` (moving
whole 24-hour chunks of the *time* field into days) as a pre-step, but
goopg's v0 `KindInterval` Datum has no time field at all — it is always
exactly zero — so that step is permanently a no-op here and
`justify_interval` collapses to plain `justify_days`. For the same reason
`justify_hours()` itself is always the identity for goopg (nothing ever
moves from a nonexistent time field into days) and is dispatched straight to
`evalExpr` by the `evalFuncCall` switch instead of through
`evalJustifyInterval` at all.

The month/day rebalancing arithmetic was factored into a standalone
`justifyIntervalDays(months, days int32) (int32, int32)` so the sign-
reconciliation branches (only reachable with a mixed-sign months+days value)
have a direct unit-test seam — goopg has no `interval + interval` operator
yet, so a SQL-level test can't construct such a value from typed literals.
Verified live against real PostgreSQL 18.3: `justify_days('35 days')` = `'1
mon 5 days'`, `justify_days('-35 days')` = `'-1 mons -5 days'`,
`justify_interval('5 mons -33 days')` = `'3 mons 27 days'`,
`justify_interval('-5 mons 33 days')` = `'-3 mons -27 days'`.

Tests: `internal/executor/interval_justify_test.go`
(`TestJustifyIntervalFunctions` — SQL-level, single-field literals;
`TestJustifyIntervalDaysSignReconciliation` — direct calls into
`justifyIntervalDays` for the mixed-sign cases). Confirmed non-vacuous via
`git stash` on `expr.go` alone (test file fails to compile without
`justifyIntervalDays`). Gates: `go build ./...` clean; `go test
./internal/executor/... ./internal/planner/... ./internal/analyzer/...
./internal/parser/...` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS (0
failed transactions, all 3 workloads).

## Cross-references

- TPC-H query bodies: HammerDB upstream
  `tpc-h/queries-93-orig.sql`.
- Parser expression AST: [root-0010-parser.md](root-0010-parser.md).
- Executor evaluator: [root-0012-executor.md](root-0012-executor.md).
- Earlier M0003 doc on NUMERIC/CASE plumbing:
  [0003-0004-hammerdb-tpch-integration.md](0003-0004-hammerdb-tpch-integration.md),
  [0003-0005-case-expressions.md](0003-0005-case-expressions.md).
