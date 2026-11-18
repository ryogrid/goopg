# M0149\-0001h1a60 — generate\_series over numeric and timestamp values

## Problem

`generate_series` only knew integers. A timestamp call read its arguments as numbers and returned
one row holding 0, the numeric form lost its fractions in the target list, and the form with a
time zone argument was rejected. A NULL argument produced rows, and a `LATERAL` call with a column
argument failed with `column ref a/0 on nil slot`, for integers too.

## What PG does

The overloads are `int4`, `int8`, `numeric`, `timestamp` and `timestamptz`, the last with an
optional zone. All are strict. The numeric form rejects NaN and infinity. The timestamp forms reject
a zero or infinite step, and each step is ordinary interval addition, so `timestamptz` follows
the zone across DST changes.

## Design

- `generateSeriesType` follows function resolution. An interval step or a zone argument selects the
  timestamp forms. A timestamp argument selects `timestamp` unless a timestamptz or a zone is
  present; otherwise the preferred type `timestamptz` applies. Three untyped literals are ambiguous,
  as in PG.
- `newGenerateSeriesCol` is the one place that reads the argument list, for the FROM form and the
  target list, and gives literals the parameter type so EXPLAIN shows them that way.
- `datumSeries` runs the numeric and timestamp forms for both executor paths. The integer path
  keeps its fast loop.
- `generateSeriesOp` implements `BindLateralOuter`, and `nodeReferencesOuter` recognises column
  arguments, so a `LATERAL` call runs once per outer row.

## Not covered

- `timestamptz` compared with an unknown literal reads the literal in UTC \(M0149\-0001h1a63\).
- `pg_typeof(generate_series(...))` returns one row \(M0149\-0001h1a64\).
- A number times an interval is rejected \(M0149\-0001h1a65\).
- Row estimates for the numeric and timestamp forms have not been compared with PG's support
  functions \(M0149\-0001h1a66\).
