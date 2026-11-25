# M0149\-0001h1a67 — date and time array input

## Problem

goopg carries an array as its text form, and a cast to a date or time array kept the text the
user wrote. `'{2020-07-01}'::timestamptz[]` printed `{2020-07-01}`, a bad element was accepted, and a
later comparison read the element without the session time zone. Storing such an array read
timestamptz elements as UTC.

## What PG does

`array_in` runs every element through the element type's input function. `array_out` prints each one
through the output function and quotes it when needed.

## Design

`normalizeDatetimeArray` \(`array_datetime.go`\) does both steps on the text, for `date`, `time`,
`timetz`, `timestamp`, `timestamptz` and `interval`. It keeps nesting and NULL elements, and it
raises PG's errors for a bad element or a malformed literal. Literals with explicit bounds are
left as they are.

It runs from three places:

- `evalCast` to one of these array types;
- `ARRAY[...]` when an element is a date or time value, so an untyped element takes the type too;
- comparison and containment operators, where the planner casts a bare literal opposite such an
  array.

`encodeArrayElem` reads `date`, `timestamp` and `timestamptz` elements through `evalCast` when a
session is available, so stored values use the session zone and DateStyle.

`timetz` values now have a text form in `formatDatumDateStyle`. Interval input skips `@`, which the
verbose output style puts before each field.

## Not covered

- `ago` in interval input is rejected \(M0149\-0001h1a69\).
- A `timetz` offset with seconds is not read \(M0149\-0001h1a56\).
