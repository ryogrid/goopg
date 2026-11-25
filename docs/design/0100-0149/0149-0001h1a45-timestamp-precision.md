# M0149\-0001h1a45 — `timestamp(p)` rounds values

## Problem

`timestamp(p)` and `timestamptz(p)` columns and casts kept every fractional digit, so
`'2020-01-01 00:00:01.6'` in a `timestamp(0)` column stayed `00:00:01.6` where PG 18.3 stores
`00:00:02`.

## What PG does

`AdjustTimestampForTypmod` \(`timestamp.c`\) rounds to the precision with the same scales as
`AdjustTimeForTypmod`: half away from zero, measured from the PG epoch of 2000\-01\-01. A value
before 2000 therefore rounds toward the past at a half. The infinities are left alone.

## Design

- `datetime.AdjustTimestampForTypmod` holds the rounding.
- `roundTimestampDatumToPrecision` applies it to a Datum, keeping the timestamptz tag.
- The value is rounded where `time(p)` already is: the insert and update coercion, so
  `RETURNING` shows the stored value; the column encoder, as the storage choke point for
  defaults and COPY; and the cast evaluator.

### Precision 0 in a cast

`CastExpr.Typmod == 0` means "no modifier", which made `::timestamp(0)` and `::time(0)` no\-ops.
`encodeTypmod` now stores \-1 for an explicit precision 0 on these types. Every existing check
of the form `Typmod > 0` keeps treating it as no length, `Typmod != 0` correctly sees a modifier,
and `CastExpr.FractionalPrecision` is the one reader of the value.

### Output and EXPLAIN

The wire formatter printed `infinity` as a date in 2262, for every timestamp value. It now
prints `infinity` and `-infinity`. EXPLAIN prints a `timestamp` literal as the Const PG holds,
normalised and rounded, with the name `timestamp(p) without time zone`.

## Not covered

- EXPLAIN prints those literals in ISO style regardless of `DateStyle`.
- A cast to a domain over a time or timestamp type prints with six fractional digits
  \(M0149\-0001h1a47\).
- Timestamps outside 1677 to 2262 are rejected \(M0149\-0001h1a48\). The regress counts for
  `timestamp` and `timestamptz` rose because of this: their widest expected rows are such dates,
  and the old wrong `infinity` text happened to give the column the expected width.
