# M0149\-0001h1a63 — untyped literals compared with timestamps

## Problem

A bare literal compared with a timestamptz stayed text until the comparison, where it was parsed
with no session zone, so it meant UTC. With `TimeZone` set to `America/New_York`,
`t = '2020-01-01'` missed the row holding local midnight. Opposite a plain timestamp the literal's
own zone was applied, where PG reads and discards it.

## What PG does

`coerce_type` turns an unknown literal into the other operand's type during parse analysis, through
that type's input function, so `timestamptz_in` sees the session zone.

## Design

The planner gives the literal its type where it resolves the expression, as a `TypedStringLit`:

- the comparison operators, including each element of a row comparison;
- `IS DISTINCT FROM` and `NULLIF`;
- `IN` lists, and the `WHEN` values of a simple `CASE`.

`coerceTimestampOperands` does this for either operand order. Array columns are left alone.

Only spellings the executor can read are typed: those starting with a digit, and the special words.
`parsePGTimestampText` does not yet read forms such as `Tuesday, February 22, 2022`, so typing them
would turn a comparison that runs today into an input error. M0149\-0001h1a56 removes this limit.

## Not covered

- The value is computed when the statement runs. PG fixes it at parse time, so a prepared statement
  or view keeps the zone in force when it was created \(M0149\-0001h1a68\).
- Timestamp array literals keep their text, so `t = ANY ('{...}')` still compares in UTC
  \(M0149\-0001h1a67\).
- `t - '2020-01-01'` and `t + '1 day'` are rejected \(M0149\-0001h1a65\).
