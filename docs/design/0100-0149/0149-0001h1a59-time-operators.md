# M0149\-0001h1a59 — operators on time and timetz

## Problem

`'10:00'::time + '1 hour'::interval` printed `1970-01-01 11:00:00`, had no result type and did not wrap
at midnight. Most other `+` and `-` forms on `time` and `timetz` failed or returned the wrong type.

## What PG does

`pg_operator.dat` defines `time_pl_interval`, `time_mi_interval`, `timetz_pl_interval`,
`timetz_mi_interval`, `time_mi_time`, `datetime_pl` and `datetimetz_pl` \(`date.c`\). The reversed forms
such as `interval + time` and `time + date` are SQL functions that the planner inlines with the
arguments swapped, so EXPLAIN prints `(t + i)` for `i + t`.

`time` casts implicitly to interval. That is how `timestamp + time`, `date - time`, `interval - time`
and `timetz - time` resolve. An untyped operand resolves to `time` in `time - unknown` and
`unknown - time`, and to interval otherwise. `timetz + unknown` is ambiguous.

## Design

A time value carries no subtype, so it cannot be told from a timestamp at run time. The operators are
therefore chosen from static types, the way `moneyArithCall` does it.

- `parser.ResolveTimeOperator` is the single table of accepted forms, result types and errors. The
  analyzer \(`timeArithResultType`\) and the planner \(`timeArithCall`\) both use it.
- `timeArithCall` converts untyped operands, adds the implicit cast, applies the argument swap, and
  produces a `FuncCall` named after the `pg_proc` entry. Forms needing only the cast stay a `BinaryOp`.
- `internal/executor/time_operators.go` implements the functions and the time to interval cast.

`timetz` values already carry their offset, so they now print it everywhere: `Datum.Format`,
`AppendValueText` and casts to text types. That exposed two older errors, also fixed here.

- Casts from timestamptz to `time` and `timetz` ignored the session time zone, and an infinite value
  gave a time instead of NULL.
- `current_time` and `localtime` read UTC, and `current_time` had no offset.

## Verification

About 80 statements covering every operand pairing, NULL, infinity, range errors, columns, aggregates,
CTAS and EXPLAIN match PG 18.3, apart from the items below.

## Not covered \(M0149\-0001h1a74\)

- A plain `time` prints with a date in `format`, `concat` and row output.
- `time + 1` reports goopg's own error text rather than `operator does not exist`.
- EXPLAIN keeps the spelling of a time literal \(`'09:00'`\) instead of its output form.
