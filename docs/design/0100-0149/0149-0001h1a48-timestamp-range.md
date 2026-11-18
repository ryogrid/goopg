# M0149\-0001h1a48 — the full PostgreSQL timestamp range

## Problem

A `KindTime` Datum keeps its instant as int64 nanoseconds since 1970 in `Datum.Int`, which
ends at 1677 and 2262. PG 18.3 accepts timestamps from 4714\-11\-24 BC to 294276 AD. Text input
is rejected outside goopg's range, and values produced by arithmetic or decoded from disk are not
checked at all.

## Target

`Datum.Int` holds microseconds since 2000\-01\-01, PG's own representation. Microseconds since
1970 would stop near 294247 AD, short of PG's limit. The infinity sentinels stay at the int64
extremes, which are also PG's values.

## Slices

| task | content | state |
|---|---|---|
| h1a48a | every unit\-dependent read goes through helpers in `datum.go` | done |
| h1a48b | switch the unit and epoch, keeping today's accepted range | done |
| h1a48c | replace `time.Duration` arithmetic that overflows past 292 years | done |
| h1a48d | BC and long\-year output in every formatter | done |
| h1a48e | parse long years, apply PG's bounds and errors, range\-check results, open the gate | done |

The gate opens last, so a value that cannot be handled correctly is never accepted.

## Slice h1a48a

`timeToCarrier`, `timeFromCarrier`, `timeCarrierUnitsPerSecond` and `timeTZCompareKey` are the
only code that knows the unit. Spill frames and the subplan hash key use `Int` directly, and
`timezone()` computes through microseconds of the day.

To test that nothing else depends on the unit, the helpers were switched to microseconds in a
throwaway worktree. Every executor, postmaster, optimizer and catalog test passed except
`TestExecCurrentTimestamp`, which compares `now()` at nanosecond precision and is expected to
change. The compiler cannot find such sites, because `Int` remains an int64.

## Slice h1a48b

`Datum.Int` is now microseconds since 2000\-01\-01, the value PG stores, so the heap codec
copies it directly instead of converting through `time.Time`. Digits below a microsecond are
dropped when a `time.Time` becomes a Datum. `timeFromCarrier` splits seconds with floor division
so an instant before 2000 keeps a non\-negative fraction.

The infinity sentinels stay at the int64 extremes. They convert to the same instants as before,
and those instants convert back to the sentinels, so code that passes an infinity through
`time.Time` still gets an infinity.

Hash partitioning was the one persisted use of the raw value: its fallback hashed the low 32 bits
of `Int`. `hashPartitionFallbackWord` keeps the former nanosecond word for time values, so rows
already stored stay in the partition a lookup computes. This is not PG's hash \(M0149\-0001h1a50\).

## Slice h1a48c

Differences, interval addition, binary COPY and index keys work on the carrier directly, so none
of them passes through `time.Duration`, which ends at 292 years. Overflow raises PG's `timestamp out
of range` or `interval out of range`.

- **Months.** `timestamp_pl_interval` clamps to the last day of a shorter month; `time.AddDate`
  carried into the next month, so January 31 plus one month was March 2. `addMonthsClamped` ports
  the PG rule.
- **`date_bin`.** Now a port of `timestamp_bin`: any stride of days and time, rounding toward
  \-infinity, PG's errors, and an infinite source returned unchanged. It used to return NULL for a
  stride given as a literal or shorter than a day.
- **Index keys.** Timestamp keys are the carrier and date keys are PG's day count, so infinity is
  stored as PG stores it and sorts after every finite value.

An index built before this change holds infinity under its old finite key. Such an index needs
`REINDEX` if the table contains `infinity` or `\-infinity` in an indexed date or timestamp column.

## Slice h1a48d

The DateStyle formatters take month, day and clock from the value and only the year digits from
`eraYear`. They used to rebuild a `time.Time` with the display year, which turned a BC leap day into
March 1. `Datum.Format` and `AppendValueText` print ` BC` instead of a negative year.

`to_char` for dates and times is now a port of `DCH_to_char` and its template parser
\(`to_char_datetime.go`\), replacing a translation to Go layouts that handled few patterns. A
timestamptz is broken down in the session zone, and a date is midnight in that zone, because PG
resolves `to_char(date, text)` to the timestamptz form. Its tests compare with output captured from
PG 18.3 for every pattern, on BC dates, BC leap days, five\-digit years and ordinary dates.

## Slice h1a48e

Input, binary COPY, interval and day arithmetic, and `date_bin` now share PG's bounds:
`MIN_TIMESTAMP` is 4714\-11\-24 BC and `END_TIMESTAMP`, the first value past the range, is
294277\-01\-01. A value outside them raises `timestamp out of range` or `date out of range` with
SQLSTATE 22008.

Go's layouts read only four\-digit, positive years, so input with a BC marker or a longer year is
parsed with a proxy year and the real year is put back afterwards. The proxy is a leap year, so
February 29 of a leap BC year stays valid.

EXTRACT of year, decade, century and millennium and `date_trunc` of the last three use the
`timestamp_part` and `timestamp_trunc` formulas for BC years. Selectivity reads far literals in whole
seconds, because `UnixNano` overflows outside 1678 to 2261.

A bad literal in `'x'::date` is reported at the string, as `coerce_type` does.

## Not covered

- Dates from 294277 to 5874897 AD are refused, because a date shares the microsecond carrier
  \(M0149\-0001h1a54\).
- `date 'x'` puts the caret on the type name, and the DateStyle hint is missing
  \(M0149\-0001h1a55\).
- Many valid input spellings are still rejected, because `DecodeDateTime` is not ported
  \(M0149\-0001h1a56\).
