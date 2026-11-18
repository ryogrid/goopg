# M0149\-0001h1a53 — to\_char for numbers and intervals

## Problem

`to_char` on a number was an approximation built from string replacement. It truncated
where PG rounds, so `to_char(12345.678, '99999.99')` printed ` 12345.67`, and it accepted
templates PG rejects. An interval reached the same code and was formatted as a number.

## What PG does

`formatting.c` parses the template into nodes and a `NUMDesc`. Each argument type has its
own entry point, which produces a digit string, and `NUM_processor` writes it through the
template. The argument type changes the result:

- `int4` with `EEEE` goes through `float8`, and `int8` goes through numeric, so a tie such as
  12500 with one fractional digit rounds differently.
- `float4` and `float8` keep at most 6 and 15 significant digits.
- `int2` has no function of its own. It resolves to `float8`, the preferred numeric type.
- `time` has no function of its own either. It is cast to interval.

## Design

`internal/executor/to_char_number.go` ports the keyword table, `NUMDesc_prepare`,
`NUM_processor`, `NUM_numpart_to_char` and the five entry points.

The Datum does not record `int4` against `int8`, or `float8` against numeric, so `evalToChar`
reads the argument's type through `optimizer.ExprType`.

The number buffer keeps its terminating NUL. A digit pattern that runs past the end of a
shortened number writes that NUL, and the C result ends there, so the port cuts the output at
the first NUL.

Intervals use the date formatter. `newIntervalDCHValue` is `interval2itm`. Fields may be
negative, widths grow by one for the sign, years are not adjusted for BC, and the ISO fields
are computed from the raw values. Patterns under `INVALID_FOR_INTERVAL` raise 22007.

`evalToChar` used to turn an argument error into NULL. It now returns the error.

## Verification

About 7,800 statements over every number type, intervals and `time` were run on goopg and
PG 18.3 with identical results and errors. `testdata/to_char_pg18.tsv` keeps a sample of
that output for `TestToCharMatchesPG18`.

## Not covered

- `L`, `G`, `D` and `S` use C locale values, because `lc_numeric` and `lc_monetary` are not
  consulted \(M0149\-0001h1a58\).
- `to_number` is a separate function and is not changed here.
