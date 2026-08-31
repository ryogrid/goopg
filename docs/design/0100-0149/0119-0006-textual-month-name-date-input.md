# M0119-0006 (40th slice) — textual month names on date/timestamp INPUT

status: accepted
date: 2026-08-11
scope: `internal/pgdatetime` (new `monthname.go`), reaching every text→date/
timestamp/timestamptz input path in `internal/executor/copy_text.go`

## The gap

`'May 1, 2002'::date` was a syntax error on goopg and is an ordinary date on
PostgreSQL. So were `'1-Jan-2020'`, `'2002-May-1'`, `'1/May/2002'` and
`'sept 1 2002'` — every spelling in which the month is written out rather than
numbered.

goopg parses date/time text with fixed Go layouts (`pgTimestampLayouts`,
`time.Parse("2006-01-02")`), and `internal/pgdatetime` exists to reconcile that
with PostgreSQL's field-at-a-time decoder by REWRITING the input into the one
canonical spelling those layouts accept. Its package doc named the textual month
as one of four deliberately-unhandled gaps (M0125-0007). This slice closes that
one; the other three (the DateOrder-dependent numeric orders, `/` between
NUMERIC fields, the 3-digit day-of-year) stay open.

The failure mode is worse than an error on one path. `tryParseStringAs`
(the cross-kind comparison coercion) reports a failed parse as "leave it a
string", so `d_date = 'May 1, 2002'` did not raise — it matched zero rows in
silence. That is the exact shape of M0125-0007, where three TPC-DS queries
returned a wrong answer with no diagnostic.

## Why a textual month needs no DateStyle

goopg does not model the `DateOrder` component of DateStyle on input at all
(`padDateFields` refuses a 1-or-2-digit leading field for precisely that
reason). The textual month is nonetheless implementable today, because spelling
the month out is what REMOVES the order ambiguity — and upstream says so
directly. `DecodeDate` (`postgres/src/backend/utils/adt/datetime.c`) runs two
passes over the split fields:

> look first for text fields, since that will be unambiguous month

…and only then "pick up remaining numeric fields". So the month is already in
`fmask` when the first numeric run reaches `DecodeNumber`, no matter where in
the string it was written, and `'May 1 2002'`, `'1-May-2002'` and `'2002-May-1'`
all take the identical path. `DecodeNumber`'s `case DTK_M(MONTH)` arm carries
upstream's own statement of intent:

> We want to support the variants MON-DD-YYYY, DD-MON-YYYY, and YYYY-MON-DD as
> unambiguous inputs.

## The rule implemented

With the month known, the two numeric runs are assigned:

| run | upstream arm | reading |
|-----|--------------|---------|
| first | `case DTK_M(MONTH)`, `flen >= 3 \|\| DateOrder == YMD` | year |
| first | same arm, else | day |
| second | `case DTK_M(YEAR)\|DTK_M(MONTH)` | day |
| second | `case DTK_M(MONTH)\|DTK_M(DAY)` | year |

`normalizeTextualMonthDate` reproduces that with the YMD arm dropped: a leading
run of 3+ digits is the year and the other run is the day; otherwise the first
run is the day and the second is the year. A year run of 1–2 digits is windowed
onto 1970..2069 (`ValidateDate()`'s `is2digits` handling), suppressed under a BC
suffix exactly as `expandRunTogetherDate` does it.

The two arms NOT ported are both gated on `DateOrder == DATEORDER_YMD`: the
branch that would read a short leading run as a year, and the
`flen >= 3 && *is2digits` swap that repairs it afterwards. Under the MDY default
goopg assumes, `'02-May-1'` is day 2 of year 1 → **2001-05-02**, which is what
PG 18.3 answers at `DateStyle = ISO, MDY`.

Month spellings are the 21 `MONTH` rows of `datetktbl`, matched exactly and
case-insensitively. There is no prefix rule: `sept` is its own row, so `sept`
decodes and `septem` does not.

## Placement

`normalizeInput` tries `padDateFields`, then `expandRunTogetherDate`, and now
the textual-month scan third — but against the WHOLE trimmed string rather than
the `datePart` the numeric arms use, since `'May 1, 2002'` contains a space
inside its own date. The scan is gated on the `runTogetherDate` flag, i.e. it is
reachable only from `NormalizeDateTimeInput` (DecodeDateTime's context) and not
from `NormalizeInput` (DecodeTimeOnly's) — `'May 1, 2002'::time` is a syntax
error upstream and must stay one.

Everything downstream is unchanged: the emitted token is the ordinary
`YYYY-MM-DD`, so `validateDateTokenFull` gives `'2002-Feb-30'` PG's 22008 rather
than a 22007, and the BC leap-day fallbacks, the zone rules (`tsZoneMode`) and
the hour-24 carry all apply as they already did.

## Two refusals that are deliberate, not omissions

- **The ISO `T` after a textual month.** `'2002-May-1T10:20:30'` is an
  ERROR on PG 18.3 (`ParseDateTime` hands the whole thing to `DecodeDate`, whose
  splitter reads `1T10` as a digit run plus an alpha run that is no month) even
  though `'2002-05-01T10:20:30'` parses. The normaliser therefore requires the
  remainder to start with whitespace.
- **`:` inside the date token.** PG only reaches `DecodeDate` for a field
  already classified as a date; scanning raw input here would otherwise let
  `'10:00 May'` — a time plus a month, an error upstream — build the date
  2000-05-10. The separator set is restricted to `- / . , space tab`.

## Verification

Every expectation was probed against the PG 18.3 reference cluster (port 65432,
`DateStyle = ISO, MDY`) rather than derived. Gates:
`internal/pgdatetime` (`TestNormalizeDateTimeInputTextualMonth`,
`TestNormalizeInputTextualMonthIsDateTimeOnly`), `internal/executor`
(`TestParsePGDateTextTextualMonth*`, `TestParsePGTimestampTextTextualMonth`,
`TestTryParseStringAsTextualMonthDate`), `TestPort_RegressSuite`, the units
pre-commit scope, `scripts/tpch-spotcheck.sh`, and the pgbench smoke.

## Deferred (ledger rows filed)

1. **A third numeric field.** `'May 1 2 2002'` is `2002-05-01 20:02:00` to PG:
   `1` is the day, `2` is a 2-digit year windowed to 2002, and the leftover
   `2002` is decoded by `DecodeNumberField` as a run-together TIME. goopg's scan
   stops once the date is complete, leaving `2002` in the remainder where no
   time layout matches — the input errors instead of decoding.
2. **A 3+-digit day gets 22007 where PG gives 22008.** `'2002-May-100'` is a
   field-range error upstream (`DecodeNumber` takes 100 as the day and
   `ValidateDate` rejects it). The normaliser declines the shape instead,
   because the emitted token has to keep the fixed `...-MM-DD` width
   `DateTokenMonthDay` reads. This is the same pre-existing divergence
   `padDateFields`' `len(d) > 2` guard already has for `'2002-5-100'`, and both
   want the same fix.
3. **The planner-side duplicate is untouched.** `internal/pgnodes/datum.go`'s
   `parseDateFields` splits on `-` and demands the year first, so a textual
   month is not const-foldable even though the executor now reads it.
