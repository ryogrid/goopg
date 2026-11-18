# M0149\-0001h1a56 — date and timestamp input parser

## Problem

Date and timestamp input went through a list of Go layouts with a set of normalising steps around
it. It read ISO spellings and little else: `Feb 10 17:32:01 1997 PST`, `19970210 173201` and
`1997/02/10` failed, DateStyle was ignored, and zone names and abbreviations were not read.

## What PG does

`date_in`, `timestamp_in` and `timestamptz_in` share `ParseDateTime` and `DecodeDateTime`
\(`datetime.c`\). The first splits the text into typed fields, and the second works through them with a
bit mask of what has been seen, so most field orders are accepted.

## Design

`internal/utils/adt/datetime/decode.go` ports `ParseDateTime`, `DecodeDateTime`, `DecodeDate`,
`ValidateDate`, `DecodeTime`, `DecodeNumber`, `DecodeNumberField`, `DecodeTimezone` and the token table.
`zone_abbrevs_gen.go` is generated from `postgres/src/timezone/tznames/Default`.

The session reaches the decoder through `DecodeEnv`: the DateStyle field order, the statement
time, the session zone, and two functions from `misc` for zone lookup and `DetermineTimeZoneOffset`.
An abbreviation is looked up in the session zone first and then in the table, as in PG. A zone's
abbreviations come from walking its periods with `Time.ZoneBounds`, cached per location.

In `copy_text.go`, `decodeTimestampText` and `decodeDateText` turn the fields into a value and replace the
layout table, the proxy\-year fallbacks and the verbose\-format fallback. Literals, casts, COPY and row
encoding all use them.

`Decoded.UsedSession` is set when the zone or DateStyle decided the result. A literal's value is then not
cached on the plan node, because the same plan can run under another setting.

With the parser in place the planner types every untyped literal opposite a timestamp, so a bad one raises
PG's input error. `dateTimeInputError` also gives PG's codes for a bad zone, an out\-of\-range displacement, and
a month or day overflow, which carries the DateStyle hint.

## Verification

About 2,000 inputs were run as `date`, `timestamp` and `timestamptz` under the three field orders on goopg and
PG 18.3. Results and errors match, apart from dates after 294276 AD \(M0149\-0001h1a54\).

## Not covered

- `time` and `timetz` input still use their own parser, not `DecodeTimeOnly`, and `timezone_abbreviations` is
  always the `Default` set \(M0149\-0001h1a70\).
- `date 'x'` puts the error caret on the type name \(M0149\-0001h1a55\).
