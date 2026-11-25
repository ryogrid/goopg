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

## Zone names

`lookupTimeZoneName` resolves a name as `pg_tzset` does, through `misc.CanonicalZoneName`: a tz database
name in any case, or an upper\-cased POSIX specification. The zone tokens of date/time input, `AT TIME ZONE`
on timetz and `timetz` input use the same lookup \(M0149\-0001h1a61\).

## pg\_timezone\_names and pg\_timezone\_abbrevs \(M0149\-0001h1a76\)

`pg_timezone_names` was a fixed list of about thirty synthetic rows. `misc.TimezoneNames` now walks the tz database
as `pg_tzenumerate_next` does: hidden entries are skipped, a file must carry a TZif header, and a leap\-second zone
\(a non\-zero leap count, `pg_tz_acceptable`\) is left out; each zone gives its abbreviation, offset and DST flag at
the current time.

`pg_timezone_abbrevs` follows its view definition: the session zone's all\-letter abbreviations
\(`pg_timezone_abbrevs_zone`, `datetime.ZoneAbbrevRows`\) and then the Default set without those names
\(`datetime.DefaultAbbrevRows`\). A dynamic abbreviation that the zone data no longer uses takes the zone's offset,
as `DetermineTimeZoneAbbrevOffsetInternal` does. The scan reads the session TimeZone.

utc\_offset is still rendered in postgres\_verbose, the style pg\_regress uses, because goopg has no IntervalStyle
output \(M0149\-0001h1a89\).

## Not covered

- `generate_series` over timestamps returns wrong rows \(M0149\-0001h1a60\).
