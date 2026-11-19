# M0149\-0001h1a51 — timestamptz arithmetic in the session time zone

## Problem

`timestamptz + interval` added months and days in UTC and returned a plain timestamp. With
`TimeZone` set to `America/New_York`, adding one day across the March DST change landed an hour
off and printed no zone. `date_bin` on timestamptz also printed no zone, and `date::timestamptz`
used midnight UTC.

## What PG does

`timestamptz_pl_interval_internal` \(`timestamp.c`\) does the month step and the day step
separately. Each one breaks the value down in the zone, changes the wall clock and converts back
with `DetermineTimeZoneOffset`, which settles a time inside a DST change. The time part is then
added as elapsed microseconds. `date_add` and `date_subtract` run the same code with an optional
named zone.

## Design

- `addTimeInterval` keeps the plain timestamp path. For a timestamptz it calls
  `addMonthsDaysInZone`, which uses the existing `misc.TimestampToTimestampTZ` port of the DST
  rule. The result keeps `TimeSubTimestampTZ`. The session zone is read only for a timestamptz, so
  other timestamp arithmetic pays nothing.
- `evalDateAdd` implements `date_add` and `date_subtract`. `lookupTimeZoneName` accepts an
  abbreviation, a tz database name or a POSIX specification.
- `evalDateBin` follows function resolution: a timestamp argument selects the timestamp form unless
  the other argument is a timestamptz, and otherwise the preferred type timestamptz is used. That
  includes dates and unknown literals.
- A date cast to timestamptz is midnight in the session zone, and a date cast to timestamp becomes a
  timestamp.
- `exprType` reports timestamp and timestamptz for these operators, `date_add`, `date_subtract` and
  `date_bin`, and interval for an interval literal.

## Not covered

- `generate_series` over timestamps returns wrong rows \(M0149\-0001h1a60\).
- Zone names are matched case\-sensitively in `date_add` \(M0149\-0001h1a61\).
