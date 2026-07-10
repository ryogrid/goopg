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

### Update: sub-day folding once the carrier gained a time field (2026-07-11)

The paragraph above rests on the premise *"goopg's v0 `KindInterval` Datum has
no time field at all"*. That premise expired on 2026-07-11 when the
timestamp − timestamp work packed sub-day microseconds into the carrier's
reserved `Hi` word (`Datum.IntervalMicrosValue` / `NewIntervalDatumFull`) and
sub-day interval literals (`interval '27 hours'`) began populating it. The
justify functions were a stale sibling of that carrier change: `justify_hours`
and `justify_interval` were still no-ops for the time field, silently dropping
or failing to normalize any sub-day component.

Corrected: `evalJustifyInterval` was renamed to `evalJustify(name, …)` and now
dispatches all three functions over the full month/day/**micros** triple:

- `justify_hours` → `justifyIntervalHours`: `TMODULO` the micros field by
  `USECS_PER_DAY`, add the whole-day quotient to `day`, then equalize the sign
  of `day` vs `time`. Mirrors `interval_justify_hours`.
- `justify_days` → unchanged `justifyIntervalDays` (month/day only; the time
  field is deliberately left untouched, exactly as upstream `interval_justify_days`).
- `justify_interval` → `justifyIntervalFull`: pre-justify days when `day` and
  `time` share a sign (upstream's overflow-avoidance step), fold 24h chunks of
  time into days, fold 30-day chunks of days into months, then equalize the
  sign of month-vs-day and day-vs-time. Mirrors `interval_justify_interval`.

int32 `day`/`month` overflow raises SQLSTATE `22008` *"interval out of range"*
(`errIntervalRange` via `addDayS32`), matching PG's `pg_add_s32_overflow` guard
— a large time field can produce a whole-day count outside int32 range.

Verified live against real PostgreSQL 18.3 (`postgres/local_install`, port
5599): `justify_hours('27 hours')` = `'1 day 03:00:00'`,
`justify_hours('2 days -1 hours')` = `'1 day 23:00:00'`,
`justify_interval('1 mon 33 days 27 hours')` = `'2 mons 4 days 03:00:00'`,
`justify_interval('1 mon -1 hours')` = `'29 days 23:00:00'`,
`justify_interval('29 days 25 hours')` = `'1 mon 01:00:00'`,
`justify_interval('-35 days -25 hours')` = `'-1 mons -6 days -01:00:00'`.
Tests: 12 new sub-day rows in `TestJustifyIntervalFunctions`. Gates: `go build`/
`go vet` clean; full executor suite PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); pgbench smoke via pre-commit hook.

## Follow-up: `isfinite()` NULL propagation (M0122-0018, 2026-07-08)

`evalIsFinite` (`internal/executor/expr.go`) computed its result as
`NewBoolDatum(!d.IsNull())` — for a NULL argument this evaluates to
`NewBoolDatum(false)`, i.e. a SQL `FALSE`, not SQL `NULL`. `isfinite` has no
`NotStrict` marker on any of its `pg_proc_seed_data.go` OIDs (1373 date, 1389
timestamp, 1390 interval, 2048 timestamptz), so — like every other strict
PostgreSQL function — a NULL argument must propagate to a NULL result rather
than being evaluated at all
(`postgres/src/backend/utils/fmgr/fmgr.c`'s `FunctionCallInvoke` strict-check,
which upstream's `date_finite`/`timestamp_finite`/`interval_finite` C
functions rely on instead of checking for NULL themselves). Fixed by checking
`d.IsNull()` before computing the result and returning `NullDatum` in that
case; the non-NULL case is unchanged (goopg v0 stores no infinity values for
any of these types, so it is unconditionally `TRUE`, as established when
`isfinite` was first stubbed under M0097-0004).

Tests: `internal/executor/isfinite_test.go`
(`TestIsFiniteNullPropagates` — SQL-level, all 4 wired OIDs' NULL case plus
one non-NULL case each for date/interval). Confirmed non-vacuous via `git
stash` on `expr.go` alone (the 4 NULL cases fail — `IsNull() = false, want
true` — without the fix). Gates: `go build ./...` clean; `go test
./internal/executor/...` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke bash
scripts/ralph-precommit-test.sh` PASS (0 failed transactions, all 3
workloads).

## Follow-up: `timestamp − timestamp → interval` + sub-day carrier (2026-07-11)

The original scope deferred `timestamp − timestamp` (upstream returns an
interval) "until the type system", along with sub-day interval units. This
follow-up landed the **subtraction + sub-day storage** half without the
sub-day *literal* parser work:

- **Carrier.** `KindInterval` now packs the sub-day microsecond field in the
  previously-reserved `Datum.Hi` word (`IntervalMicrosValue()` /
  `NewIntervalDatumFull(months, days, micros)`), mirroring upstream's
  months/days/microseconds `Interval` struct. Month/day-only literals leave
  `Hi = 0`, so every pre-existing caller is unaffected (purely additive).
- **Output.** `formatInterval` was rewritten to mirror `EncodeInterval`
  under the default `INTSTYLE_POSTGRES`
  (`postgres/src/backend/utils/adt/datetime.c`), including the
  `[-|+]HH:MM:SS[.ffffff]` time component (hours may exceed 24; fractional
  seconds trimmed of trailing zeros; PG's per-field `is_before` sign quirk).
  **Verified byte-for-byte against real PostgreSQL 18.3** — e.g. `2 days`,
  `1 day 12:00:00`, `-1 days -12:00:00`, `-1 mons +02:00:00`, `25:00:00`,
  `00:00:00.5`.
- **Arithmetic.** `evalBinary` (`internal/executor/expr.go`) gained a
  `KindTime − KindTime` branch → `subTimeTime` (microsecond delta justified
  into whole 24h days, à la `timestamp_mi` + `interval_justify_hours`) and an
  `interval ± interval` branch → `addIntervalInterval` (`interval_pl` /
  `interval_mi`). `addTimeInterval` now also applies the micros component.
- **Analyzer.** `timestamp/time/date` subtraction now types as `interval`
  (was rejected `42725 "operator is not unique"`); `interval ± interval` types
  as `interval` (was rejected `42804`). Addition of two temporal values is
  still `42725`, matching PG.
- **Ancillary micros plumbing.** interval comparison (`interval_cmp_value`
  decomposition), sort/hash spill encode/decode, and the group-by key builder
  were all extended to carry the microsecond field so a sub-day interval never
  silently collapses when it flows through ORDER BY / GROUP BY / spill.

**Still deferred** (see `deferral_ledger.md`): (1) sub-day interval *literal*
syntax (`interval '2 hours'`) — a parser change across the Form 1 / Form 2
unit switches plus `evalIntervalLit` / `parseIntervalCastString`; (2)
`date − date → integer` — goopg models DATE internally as a timestamp and does
not flag date literals, so `date − date` yields an `interval` ("9 days")
rather than upstream `date_mi`'s `int4` day count.

Tests: `internal/executor/interval_subday_test.go`
(`TestFormatIntervalSubDay` unit + `TestTimestampSubtractionInterval` E2E).
Gates: `go build ./...` / `go vet` clean; `go test ./internal/executor/...`
`./internal/analyzer/...` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh`
PASS (0 failed transactions).

## Follow-up: sub-day interval *literal* parsing (unimplemented_feat #5, 2026-07-11)

The prior follow-up landed the sub-day *storage* half but left the deferred item
(1) above — the *literal* grammar. This loop closed it: sub-day interval literals
now parse end-to-end through all four unit-parsing paths, so
`interval '2 hours'`, `interval '90 minutes'`, `interval '2' hour`,
`'3 hours'::interval`, and `CAST('15 minutes' AS interval)` all yield a
`KindInterval` carrying the microsecond component, and arithmetic composes them
with day/hour parts (`interval '1 day' + interval '2 hours'` → `1 day 02:00:00`).

- **Four switches, one conversion.** `hour/minute/second/millisecond` (+plural)
  cases were added to `internal/parser/select.go` Form 1 (trailing-unit
  `interval '<N>' <unit>`) and `splitEmbeddedInterval` Form 2
  (`interval '<N> <unit>'`), and to `internal/executor/expr.go` `evalIntervalLit`
  (typed literal) and `parseIntervalCastString` (`::interval` / `CAST` cast). Each
  converts the integer magnitude to microseconds via `NewIntervalDatumFull(0,0,µs)`
  using new `usecsPerHour/Minute/Second/Milli` consts (siblings of the existing
  `usecsPerDay`). `parseIntervalCastString`'s signature grew a `micros int64`
  return; its sole caller now builds the Datum with `NewIntervalDatumFull`.
- **Rendering is reused, not re-derived.** Output rides the already-PG-verified
  `formatInterval` time component — no new formatting code — so the assertions in
  the new test inherit the loop-#16 byte-for-byte PG 18.3 verification.
- **Analyzer untouched.** It already types every `IntervalLit` as `interval`.

**Still deferred** (see `deferral_ledger.md`): (a) fractional magnitudes
(`interval '1.5 seconds'`) — the literal body is parsed with `strconv.ParseInt`,
so a decimal raises `22007`; PG spills the fraction into the next-smaller unit;
(b) multi-field literals (`'1 day 05:00:00'`, `'1 year 2 mons'`, `HH:MM:SS`
bodies) — `splitEmbeddedInterval` requires exactly two whitespace fields; a real
`DecodeInterval`-style tokenizer is needed; (c) week/decade/century/microsecond
units. Item (2) `date − date → integer` from the prior follow-up is unchanged.

Tests: `internal/executor/interval_subday_test.go` `TestSubDayIntervalLiterals`
(Form 1, Form 2, cast, sub-day arithmetic, `timestamp + interval` literal).
Gates: `go build ./...` clean; `go test ./internal/executor/... ./internal/analyzer/...
./internal/planner/...` and `./internal/parser/...` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook.

## Follow-up: fractional interval magnitudes + Form-1 typmod truncation (2026-07-11)

The prior follow-up left deferred item (a): fractional magnitudes
(`interval '1.5 hours'`). This loop closed it and, in doing so, surfaced a
second, distinct PostgreSQL semantic that the integer-only path had masked —
the **trailing-unit form is an SQL interval typmod that truncates**.

- **Fractional parsing (`parseIntervalMagnitude`).** The literal body is split
  into an integer part (`val int64`) and a fractional part (`fval float64`,
  |fval|<1, sharing val's sign) — mirroring how PG's `DecodeInterval` feeds a
  numeric field to its `Adjust*` helpers
  (`postgres/src/backend/utils/adt/datetime.c`). `"1.5"→(1,0.5)`,
  `"-1.5"→(-1,-0.5)`, `".5"→(0,0.5)`.
- **Fractional spill (`intervalUnitToParts`).** A single shared helper — now the
  sole source of truth for both `evalIntervalLit` (typed literal) and
  `parseIntervalCastString` (`::`/CAST), unifying the two sibling paths — spills
  the fraction into smaller units exactly as PG does: fractional **years →
  months** (`rint(fval*12)`, sub-month discarded, round-half-to-even via
  `math.RoundToEven`), fractional **months → days** (`int(fval*30)`) + remainder
  micros, fractional **days → micros** (`fval*USECS_PER_DAY`), and
  **h/m/s/ms → micros** (`(val+fval)*scale`). `intervalFractMicros` reproduces
  `AdjustFractMicroseconds`'s truncate-then-round-remainder (`>0.5`/`<-0.5`
  strict; an exact half stays truncated).
- **Form-1 typmod truncation (`truncIntervalToUnit`).** `interval '1.5' hour`
  yields `01:00:00`, *not* `01:30:00`: the trailing unit is an SQL typmod field
  that truncates the value to that field's granularity (PG's
  `AdjustIntervalForTypmod`, `timestamp.c`), toward zero for negatives. This is
  distinguished from the typmod-free embedded-string form `interval '1.5 hours'`
  (→ `01:30:00`) by a new `Qualified` flag on both `parser.IntervalLit` and
  `planner.IntervalLit`, set only in the parser's Form-1 branch and threaded
  through the two `planner.go` conversions + `plpgsql_runtime.go`. Integer
  literals are unaffected (nothing below the field to drop), so no regression to
  the loop-#17 integer cases.

Every expected value was captured byte-for-byte from a real PostgreSQL 18.3
instance and re-verified end-to-end against a live `cmd/goopg` server + `psql`
(`interval '1.5 hours'`→`01:30:00`, `interval '1.5 months'`→`1 mon 15 days`,
`interval '1.5 years'`→`1 year 6 mons`, `interval '1.15 months'`→
`1 mon 4 days 12:00:00`, `interval '1.5' hour`→`01:00:00`, `interval '1.9' hour`→
`01:00:00`, `interval '1.5' year`→`1 year`, `interval '-90.9' minute`→
`-01:30:00`, `'2.5 days'::interval`→`2 days 12:00:00`).

**Still deferred** (see `deferral_ledger.md`): (b) multi-field literals
(`'1 day 05:00:00'`, `'1 year 2 mons'`, bare `HH:MM:SS` bodies) — a real
`DecodeInterval` tokenizer; (c) week/decade/century/microsecond unit names;
(d) the full interval-typmod feature beyond a single trailing field — range
qualifiers (`HOUR TO MINUTE`), `SECOND(p)` precision, and PG's treatment of a
non-standard trailing word (`interval '1.5' millisecond`) as a *column alias*
over a bare `interval '1.5'` (which defaults to **seconds** → `00:00:01.5`),
whereas goopg's Form-1 switch still recognizes `millisecond`/`milliseconds` as a
unit (a pre-existing loop-#17 divergence, unchanged here).

Tests: `internal/executor/interval_subday_test.go`
`TestFractionalIntervalLiterals` (Form-2 spill, cast spill, Form-1 truncation,
negative truncation toward zero). Gates: `go build ./...` clean; `go test`
executor/parser/planner/analyzer suites PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); pgbench smoke via pre-commit hook.

## Follow-up: multi-field interval literals (unimplemented_feat #5(b), 2026-07-11)

Closes the prior row's deferred item (b): **multi-field / `HH:MM:SS` interval
bodies now parse end-to-end** — `interval '1 day 05:00:00'`,
`interval '1 year 2 mons 3 days 04:05:06.789'`, bare `interval '05:00:00'`,
`interval '04:05'`. This is the shape goopg's own `intervalout`
(`formatInterval`) emits, so goopg can now re-parse its own interval output.

**Single-tokenizer design (the important part).** Rather than grow a second
interval parser, the pure interval-body math was hoisted into the parser
package as the single source of truth: `ParseIntervalMagnitude`,
`IntervalUnitToParts`, and the new `ParseIntervalBody` tokenizer now live in
`internal/parser/interval.go`. The executor's `evalIntervalLit` (typed-literal
path) and `parseIntervalCastString` (`::interval`/`CAST` path) — the two
*sibling paths* the practice card warns must never diverge — both delegate to
these. `parseIntervalCastString` is now a one-line call to
`parser.ParseIntervalBody`, so single-field, multi-field, and time bodies parse
identically whether they arrive as a typed literal or a runtime cast.

`ParseIntervalBody` mirrors PostgreSQL's `DecodeInterval`
(`postgres/src/backend/utils/adt/datetime.c`) for the supported field shapes:
any number of `<magnitude> <unit>` pairs interleaved in any order with
`[+-]HH:MM[:SS[.ffffff]]` time words, **each field carrying its own sign**
(`interval '-1 day 05:00:00'` = `-1 days +05:00:00`; `interval '1 day
-05:00:00'` = `1 day -05:00:00`). It accepts the intervalout abbreviations
`mon(s)`/`min(s)`/`sec(s)`/`hr(s)` in addition to the full unit words so the
round-trip works. Fractional per-field magnitudes reuse the existing spill
helpers (`interval '1.5 days 2 hours'` = `1 day 14:00:00`).

Parse-time decode: multi-field bodies are decoded once by the parser and
carried on `IntervalLit` as pre-computed `PreMonths/PreDays/PreMicros`
(`PreComputed` flag), threaded through the two `planner.go` node conversions
and `plpgsql_runtime.go` (same pattern as `Qualified`). `evalIntervalLit`
returns them directly; the trailing-unit typmod truncation never applies to
embedded forms.

**Still deferred** (see `deferral_ledger.md`): a bare trailing number with no
unit (`interval '5'` → PG `00:00:05`, the SQL interval-typmod default-unit
case) is intentionally still rejected here — that is item (d) territory;
week/decade/century unit names (item c); and single-letter unit forms
(`h`/`m`/`s`/`d`/`y`, ambiguous `m`) remain unhandled.

Every `want` captured byte-for-byte from a real PostgreSQL 18.3 instance.
Tests: `internal/executor/interval_subday_test.go`
`TestMultiFieldIntervalLiterals` (date+time bodies, bare times, per-field
signs, fractional spill, cast forms, arithmetic) and
`TestParseIntervalBodySingleFieldMatchesUnitToParts` (a sibling-path guard
asserting the multi-field tokenizer and the single-field spill helper agree on
every `<magnitude> <unit>`). Gates: `go build`/`go vet` clean;
executor/parser/planner/analyzer suites PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); pgbench smoke via pre-commit hook.

## Follow-up: coarse interval units week/decade/century/millennium/microsecond (unimplemented_feat #5(c), 2026-07-11)

Closes the prior row's deferred item (c): the coarse interval units **week,
decade, century, millennium, and microsecond** (plus the `dec`/`cent`/`mil`/
`us`/`usec` abbreviations) now parse end-to-end in interval bodies —
`interval '3 weeks'` → `21 days`, `interval '1 century'` → `100 years`,
`interval '500000 microseconds'` → `00:00:00.5`, and multi-field mixes like
`interval '1 century 2 weeks 04:05:06'` → `100 years 14 days 04:05:06`.

The change is confined to the two pure helpers in `internal/parser/interval.go`
that already own interval-body decoding — no new parser path and no
`select.go`/executor edit:

- `canonicalIntervalUnit` gains the new spellings.
- `IntervalUnitToParts` gains the scale/spill cases, mirroring PG's
  `DecodeInterval` (`postgres/src/backend/utils/adt/datetime.c`):
  week = `AdjustDays(val,7)` + `AdjustFractDays(fval,7)` (whole days then the
  leftover fraction spills to micros); decade/century/millennium =
  `AdjustYears(val,10|100|1000)` + `AdjustFractYears` (→ 120/1200/12000 months,
  fractional years rounded half-to-even into months); microsecond =
  `AdjustMicroseconds(val,fval,1)` (sub-µs fraction discarded).

Because these units are **not** SQL interval typmod qualifiers, PG parses a
trailing `WEEK`/`DECADE`/… token as a *column alias* over a bare `interval '3'`
(= 3 seconds), not a typmod field — so `select.go`'s Form-1 trailing-unit switch
is deliberately left untouched (it would otherwise mis-handle the alias case,
which is blocked on the bare-number default-unit item d-i anyway). The units
reach `IntervalUnitToParts` only via the embedded / `::interval` cast bodies
routed through `ParseIntervalBody`.

**Still deferred** (see `deferral_ledger.md`): (d-i) bare-number default-unit
(`interval '5'` → seconds); (d-ii) single-letter unit forms (`w`/`c`/`h`/`m`/
`s`/`d`/`y`, positionally-ambiguous `m`); (d-iii) full interval typmod grammar
(`HOUR TO MINUTE` ranges, `SECOND(p)` precision).

Every `want` captured byte-for-byte from a real PostgreSQL 18.3 instance. Tests:
`internal/executor/interval_subday_test.go` `TestWeekDecadeCenturyIntervals`
(embedded, multi-field, abbreviations, cast forms) plus new coarse-unit rows in
the sibling-path guard `TestParseIntervalBodySingleFieldMatchesUnitToParts`.
Gates: `go build`/`go vet` clean; executor/parser suites PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke via pre-commit
hook.

## Follow-up: bare-number default-unit → seconds (unimplemented_feat #5(d-i), 2026-07-11)

Closes the prior row's deferred item (d-i): a **unitless number in an interval
body now defaults to SECONDS** — `interval '5'` → `00:00:05`, `interval '90'` →
`00:01:30`, `interval '1 day 5'` → `1 day 00:00:05`, `interval '-5'` →
`-00:00:05`, and the fractional `interval '1.5'` → `00:00:01.5`. This mirrors
PostgreSQL's `DecodeInterval` (`postgres/src/backend/utils/adt/datetime.c`): a
`DTK_NUMBER` field with no unit resolves through the typmod `range` switch,
falling through to `DTK_SECOND` for the default full-range typmod.

Confined to the single shared tokenizer `parser.ParseIntervalBody` — no new
parser path, no executor edit — so both sibling entry points (the parser's
Form-2 typed-literal path and the executor's `::interval` / CAST path) gain the
behaviour at once.

**The subtlety — PG's right-to-left field scan.** `DecodeInterval` reads fields
*backwards* to "pick up units before values", carrying the rightmost unit
leftward, and rejects any field whose `tmask` collides with the accumulated
`fmask`. The consequence: the *only* unitless field that decodes without a
collision is a **single trailing value**. Two bare numbers, or a bare number
before a `<num> <unit>` pair, both re-use the same carried/default field and
error (`interval '1 2 days'`, `interval '5 5'` → error); and because a time word
`HH:MM[:SS]` always stamps `DTK_TIME_M ⊇ SECOND`, a trailing bare number after a
time component is a SECOND-slot collision (`interval '1 day 05:00:00 5'` →
error). goopg's left-to-right tokenizer reproduces this exactly by (1) accepting
a unitless number only as the **final** field and (2) tracking a `secondsOccupied`
flag set by any time word or explicit seconds unit — a distinct-bit unit such as
`millisecond` does *not* set it, so `interval '1 ms 5'` → `00:00:05.001` is
still accepted, matching PG's per-field-mask bookkeeping.

**Still deferred** (see `deferral_ledger.md`): the SQL year-month hyphen field
(`interval '1-2'` → 1 year 2 months, a distinct `DecodeInterval` branch);
(d-ii) single-letter unit forms (`w`/`c`/`h`/`m`/`s`/`d`/`y`, positionally
ambiguous `m`); (d-iii) full interval typmod grammar (`HOUR TO MINUTE` ranges,
`SECOND(p)` precision) including the Form-1 trailing-word column-alias
fall-through.

Every `want` captured byte-for-byte from a real PostgreSQL 18.3 instance. Tests:
`internal/executor/interval_subday_test.go` `TestTrailingBareNumberDefaultsToSeconds`
(accept cases, both sibling paths) plus the three new collision-error cases in
`internal/executor/interval_cast_test.go` `TestIntervalCastFromStringInvalidSyntax`.
Gates: `go build`/`go vet` clean; executor/analyzer/planner/parser suites PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pgbench smoke via pre-commit
hook.

## Follow-up: SQL year-month hyphen field (unimplemented_feat #5, 2026-07-11)

Closes the prior row's deferred year-month item: the **SQL-standard
"years-months" hyphen field** now parses — `interval '1-2'` → `1 year 2 mons`,
`interval '100-11'` → `100 years 11 mons`, `interval '0-5'` → `5 mons`. A
`<int>-<int>` token decodes to `years*12 ± months`, mirroring PostgreSQL's
`DecodeInterval` `DTK_NUMBER` hyphen branch
(`postgres/src/backend/utils/adt/datetime.c`): `val = strtoi64(field)`, then on
seeing a `-`, `val2 = strtoint(cp+1)` with `type = DTK_MONTH` unconditionally, so
the field contributes **months only** (never days/micros) regardless of the
typmod default. A leading `-` on the whole field flips the sign of *both*
components (`-1-2` → `-14` months = `-1 years -2 mons`), reproducing PG's
`if (*field[i] == '-') val2 = -val2` layered on strtoi64's already-negative year.

Confined to the shared tokenizer: a new pure helper `parser.parseYearMonthField`
is consulted inside `ParseIntervalBody`'s field loop *before* plain-magnitude
parsing, so both sibling entry points (the parser's Form-2 typed-literal path and
the executor's `::interval` / CAST path) gain it at once — no `select.go` or
executor edit. The month part is bounded `0 ≤ m < 12` with nothing trailing
(Go's `ParseInt` requiring a whole-string integer reproduces PG's `*cp != '\0'`
bad-format check and `val2 < 0 || val2 >= MONTHS_PER_YEAR` range check): `1-12`,
`1-13`, `1--2`, `1-2-3`, `1-2x` all error (22007). Because a year-month field
sets only the MONTH contribution and never the SECOND slot, it composes cleanly
with everything else, including a *trailing* bare number that still defaults to
seconds (`interval '1-2 3'` → `1 year 2 mons 00:00:03`) and a distinct-bit YEAR
field (`interval '1 year 1-2'` → `2 years 2 mons`).

**Still deferred** (see `deferral_ledger.md`): (d-ii) single-letter unit forms
(`w`/`c`/`h`/`m`/`s`/`d`/`y`, positionally ambiguous `m`); (d-iii) full interval
typmod grammar (`HOUR TO MINUTE` ranges, `SECOND(p)` precision) including the
Form-1 trailing-word column-alias fall-through; and the **field-mask collision
cases goopg does not model** — PG rejects a repeated MONTH bit (`1-2 3 mons`,
`1 mon 2 mons`) which goopg silently sums, and PG accepts three tokenizer
quirks goopg rejects (`1-2.5` → `1 year 2 mons 0.5s`, `1-` → `1 year`, and a
trailing lone unit word as a leftward type hint `1-2 days` → `1 year 2 mons`).
Modelling these needs a full `fmask`/`tmask` field-collision port, a distinct
feature from this bounded additive field.

Every `want` captured byte-for-byte from a real PostgreSQL 18.3 instance. Tests:
`internal/executor/interval_subday_test.go` `TestYearMonthHyphenIntervals`
(accept + compose cases, both sibling paths) plus five new bound/format error
cases in `internal/executor/interval_cast_test.go`
`TestIntervalCastFromStringInvalidSyntax`. Gates: `go build`/`go vet` clean;
executor/analyzer/planner/parser suites PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); pgbench smoke via pre-commit hook.

## Follow-up: single-letter interval unit forms (unimplemented_feat #5(d-ii), 2026-07-11)

Closes the prior rows' deferred item (d-ii): the **single-letter interval unit
abbreviations** now parse — `interval '1 y'` → `1 year`, `interval '1 c'` →
`100 years`, `interval '1 w'` → `7 days`, `interval '1 d'` → `1 day`,
`interval '1 h'` → `01:00:00`, `interval '1 m'` → `00:01:00`,
`interval '1 s'` → `00:00:01`. These are exactly the seven single-character keys
PostgreSQL's interval-decoding table carries (`deltatktbl` in
`postgres/src/backend/utils/adt/datetime.c`): `y c w d h m s`.

**The "positional ambiguity" flagged in earlier deferral rows was a
misconception, refuted by reading `DecodeInterval`.** In an interval literal `m`
is *unambiguously* MINUTE, never month: `DecodeInterval` (reading right-to-left)
resolves every unit token through `DecodeUnits`, which binary-searches
`deltatktbl` — and `deltatktbl` maps `{"m", UNITS, DTK_MINUTE}` independent of
any neighbouring field. (The `m`→MONTH mapping lives only in the *absolute-date*
table `datetktbl`, which interval decoding never consults.) So `interval '1 m'`
is one minute and `interval '1 y 2 m'` is `1 year 00:02:00` (1 year + 2
*minutes*), both confirmed byte-for-byte against PostgreSQL 18.3.

A companion fidelity point: `quarter`/`qtr` and the timezone tokens
(`tz`/`timezone`/`timezone_h`/`timezone_m`) *are* present in `deltatktbl`
(decoding to `DTK_QUARTER`/`DTK_TZ*`), but `DecodeInterval`'s per-unit `switch
(type)` has **no case** for them, so PG falls through to `default:
DTERR_BAD_FORMAT` and raises `22007`. goopg reproduces this by *not* adding them
to `canonicalIntervalUnit`, so they fall through to the same error path
(`interval '1 qtr'`/`interval '1 timezone'` → 22007).

Scope is a one-function change: seven keys appended to the existing
`canonicalIntervalUnit` switch in `internal/parser/interval.go`. Because that
helper is the shared choke point for `ParseIntervalBody`, both sibling entry
points (parser Form-2 typed literal and executor `::interval`/CAST) gain the
forms with no `select.go`/executor edit. Glued forms (`1m` with no space) remain
a separate tokenizer gap — `ParseIntervalBody` splits on whitespace, matching
neither PG's `ParseDateTime` letter/digit split nor these single-letter keys to
an unseparated magnitude; that quirk stays deferred with the other tokenizer
items below.

**Still deferred** (see `deferral_ledger.md`): (d-iii) full interval typmod
grammar (`HOUR TO MINUTE` ranges, `SECOND(p)` precision, Form-1 trailing-word
column-alias fall-through); the field-mask collision cases goopg does not model
(repeated MONTH bit `1-2 3 mons`/`1 mon 2 mons`); and the tokenizer quirks
(`1-2.5`, `1-`, glued `1m`, trailing lone-unit-word type hint `1-2 days`) — all
require a full `fmask`/`tmask` collision port plus a letter/digit tokenizer,
distinct from this additive key change.

Tests: three new blocks in `internal/executor/interval_subday_test.go`
`TestWeekDecadeCenturyIntervals` (all seven single-letter accepts, the `m`=minute
composition beside a YEAR field, and both cast/`::` sibling paths) and four new
`quarter`/`qtr`/`tz`/`timezone` reject rows in
`internal/executor/interval_cast_test.go`
`TestIntervalCastFromStringInvalidSyntax`. Every `want`/error captured
byte-for-byte from a live PostgreSQL 18.3 instance. Gates: `go build`/`go vet`
clean; executor + parser suites PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); pgbench smoke via pre-commit hook.

## Follow-up: glued `<magnitude><unit>` forms + field-mask collisions (#5(d-iii), 2026-07-11)

`interval '2h30m'`, `interval '1.5h'`, `interval '1y2mon3d'`, `interval '-5h'`
and `interval '1day'` now parse end-to-end, and the field-mask collisions
PostgreSQL rejects (`interval '1h2h'`, `interval '1 mon 2 mons'`,
`interval '1-2 3 mons'`, `interval '1h 05:00:00'`, `interval '1.5 sec 200 ms'`)
now error. This closes the "glued `1m`" tokenizer quirk and the repeated-field
collision cases the #5(d-ii) doc above left deferred.

**Tokenizer (glued split).** `ParseIntervalBody` previously split only on
whitespace, so any glued form failed magnitude parsing. It now pre-expands each
whitespace field through `expandIntervalFields`/`splitAlphaNumRuns`, reproducing
PostgreSQL `ParseDateTime`'s (postgres/src/backend/utils/adt/datetime.c) rule of
starting a fresh field at every digit↔letter boundary. The one non-obvious twist
is faithfully modelled: when an all-letter run is glued immediately before a
digit, PG consults the **absolute** `datetktbl` (not the interval `deltatktbl`)
via `datebsearch`; if the run is *not* a key there, PG swallows the run plus the
following alphanumerics into a single invalid `DTK_DATE` field and errors. The
interval-unit letters PG keeps in `datetktbl` are exactly `d`, `h`, `m`, `s`,
`y`, `mon`, `dec`, which is why `1d2h`/`1mon2d`/`1dec2d` parse but
`1day2h`/`1w2d`/`1min2sec` error. `inDatetktbl` encodes the pure-letter
`datetktbl` keys; a letter-run glued before a digit that it does not recognise
returns `ok=false` from `splitAlphaNumRuns`. Fields carrying `:` (time words) or
an internal `-` (SQL year-month, `1-2`) are left intact and decoded whole, as
PG's `DTK_TIME` / `DTK_DATE` branches do.

**Field-mask collisions (`fmask`/`tmask`).** The old `secondsOccupied bool`
tracked only the SECOND slot. It is replaced by an `intervalFieldMask` bitmask
mirroring DecodeInterval's `fmask`: each decoded field contributes a `tmask`
(`intervalUnitMask`, or `imMonth` for a year-month field, or `imTime` =
HOUR|MINUTE|all-SECOND slots for a time word), and a non-zero intersection with
the running `fmask` is a bad-format error — exactly PG's `if (tmask & fmask)
return DTERR_BAD_FORMAT`. A SECOND unit with a fractional magnitude widens to
`imAllSecs` (DTK_ALL_SECS_M), so `interval '1.5 sec 200 ms'` collides while
`interval '1 sec 200 ms'` does not. Collision detection is order-independent, so
the left-to-right scan matches PG's right-to-left scan for every shape goopg
supports.

`IntervalUnitToParts` and `canonicalIntervalUnit` keep their signatures; the mask
is computed alongside via the new `intervalUnitMask` helper, so the executor's
single-field typed-literal path (`expr.go`, which calls `IntervalUnitToParts`
directly) is unaffected and only the shared `ParseIntervalBody` gained the new
behaviour — keeping the Form-2 and `::interval`/CAST sibling paths in lock-step.

**Still deferred** (see `deferral_ledger.md`): the year-month tokenizer quirks
`interval '1-'` (→ 1 year) and `interval '1-2.5'` (→ 1 year 2 mons + 0.5 s),
where PG's `DTK_DATE`/number lexer splits inside a hyphenated field; full
interval typmod grammar (`HOUR TO MINUTE` ranges, `SECOND(p)` precision, Form-1
trailing-word column-alias fall-through); ISO-8601 duration bodies
(`interval 'P1Y2M'`); and interval `±infinity`. Every `want`/error in the new
tests was captured byte-for-byte from a live PostgreSQL 18.3 instance.

Tests: `internal/executor/interval_subday_test.go`'s new
`TestGluedIntervalLiterals` (glued accepts across both sibling paths + the
collision/gobble rejections). Gates: `go build`/`go vet` clean; parser +
executor interval suites PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
pgbench smoke via pre-commit hook.

## Follow-up: ISO 8601 duration interval bodies (unimplemented_feat #5(d-iii-rest), 2026-07-11)

Interval literals may be written as ISO 8601 durations — `interval 'P1Y2M3DT4H5M6S'`,
`interval 'PT1H30M'`, `interval 'P1.5D'` — in both the "format with designators"
and the "alternative format" (basic `P00020607T013000` and extended
`P0002-06-07T01:30:00`). PostgreSQL's `interval_in`
(`postgres/src/backend/utils/adt/timestamp.c`) tries the free-form
`DecodeInterval` first and, only when it returns `DTERR_BAD_FORMAT`, falls back to
`DecodeISO8601Interval` (`.../datetime.c`). goopg now mirrors that exactly.

**Ordering as a fallback.** The old `ParseIntervalBody` body was renamed to
`decodeIntervalFields` (the free-form decoder); `ParseIntervalBody` now calls it
first and, on failure, calls the new `decodeISO8601Interval`. A body the free-form
decoder accepts is therefore never overridden by the ISO reading — matching PG's
"if those functions think it's a bad format, try ISO8601 style" ordering. The ISO
decoder is fed the **untrimmed** body so PG's `str[0] != 'P'` guard (which rejects a
leading space) stays byte-accurate.

**Faithful port.** `decodeISO8601Interval` is a line-by-line port of
`DecodeISO8601Interval`: a `datepart`/`havefield` state machine over
`P<date>T<time>`, with `Y`/`M`/`W`/`D` designators before the `T` and `H`/`M`/`S`
after it (so `M` is months before `T` but minutes after — `interval 'P1M'` = 1 mon,
`interval 'PT1M'` = 00:01:00). Crucially, every designator field and every
extended-alternative field routes through the **shared `IntervalUnitToParts`** — the
same fractional-spill math (`AdjustFractYears`/`AdjustFractDays`/
`AdjustFractMicroseconds`) the free-form decoder already uses — so the two decoders
cannot drift. Only the two "basic" packed formats (an 8-digit `YYYYMMDD` date field,
a 6-digit `HHMMSS` time field, detected via `iso8601IntegerWidth` == 8/6) need inline
component math. New helpers `scanISONumberPrefix`/`parseISO8601Number` reproduce
`ParseISO8601Number` (strtod-style prefix scan, truncate-toward-zero integer split,
±1e15 overflow guard); `clampISOInterval` reproduces `itmin2interval`'s int32
month/day overflow check.

**Still deferred** (see `deferral_ledger.md`): full interval typmod grammar
(`HOUR TO MINUTE` ranges, `SECOND(p)` precision, Form-1 trailing-word column-alias
fall-through); interval `±infinity`; and the year-month tokenizer quirks
`interval '1-'` / `interval '1-2.5'` in the free-form decoder. Every `want`/error in
the new test was captured byte-for-byte from a live PostgreSQL 18.3 instance
(port 5599).

Tests: `internal/executor/interval_subday_test.go`'s new
`TestISO8601IntervalLiterals` (30 designator/alternative-format accepts across both
sibling paths + 6 malformed-body rejections). Gates: `go build`/`go vet` clean;
parser + executor interval suites PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); pgbench smoke via pre-commit hook.

## Cross-references

- TPC-H query bodies: HammerDB upstream
  `tpc-h/queries-93-orig.sql`.
- Parser expression AST: [root-0010-parser.md](root-0010-parser.md).
- Executor evaluator: [root-0012-executor.md](root-0012-executor.md).
- Earlier M0003 doc on NUMERIC/CASE plumbing:
  [0003-0004-hammerdb-tpch-integration.md](0003-0004-hammerdb-tpch-integration.md),
  [0003-0005-case-expressions.md](0003-0005-case-expressions.md).
