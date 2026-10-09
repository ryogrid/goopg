# M0146-0045 — ARRAY[...] output quotes its elements like array_out

Status: done (2026-10-03). Parent: M0146 (banner item 2a, third S2 batch).

## The defect

The `array_construct` evaluator (the lowered form of `ARRAY[...]`) built
`{e1,e2,…}` from each element's raw `Datum.Format()` text with no quoting.
So the printed array re-read as a different value:

| expression | goopg (before) | PG 18.3 |
|---|---|---|
| `array['a,b','c']` | `{a,b,c}` (three elements) | `{"a,b",c}` |
| `array['']` | `{}` (empty array) | `{""}` |
| `array[null::text,'NULL']` | `{NULL,NULL}` | `{NULL,"NULL"}` |
| `array['a"b']` | `{a"b}` | `{"a\"b"}` |

Dates and timestamps also printed in `Format()`'s fixed style
(`{01-02-2020}`, an unquoted `{2020-01-01 10:00:00.000000}`) instead of
their output function's.

## The fix

- Each non-NULL element's text is its output function's:
  `formatDatumDateStyle` applies the session DateStyle, the same text
  `::text` prints. That text is quoted by array_out's rule through the
  shared `array.QuoteTextElem`: an empty string, `NULL` in any case, or any
  of `{}",\` and whitespace gets double quotes, with `"` and `\` escaped.
- A sub-array element (a nested `array_construct`, an array-typed
  expression, or an untyped function result whose text is `{…}`) is
  spliced unquoted, so `ARRAY[ARRAY[…]]` stays multi-dimensional.

## Verification

- Scratch probes against PG 18.3 are identical: the reported cases,
  backslash, braces, numerics, booleans, timestamp and date elements,
  nested arrays (including a quoted element inside a nested array), text
  columns and `text[]` columns.
- `TestArrayConstructQuotesElements` pins them.
- Regress A/B: `arrays` 3205→3185, `rowtypes` 1391→1384, `jsonb`
  6517→6511, `json` 2771→2765 lines, with no new mismatch line. The one
  changed `arrays` line is the pre-existing `tuple too large` TOAST error
  at a different random-data length. `strings` is identical.
- Gates: units, spotcheck, sweep 96/96, fire set, TPC-H arm.

## Residuals

- `QuoteTextElem` treats space, `\t`, `\n` and `\r` as whitespace; PG's
  `array_isspace` also includes `\v` and `\f`.
- The untyped-function fallback treats any `{…}` text as a sub-array. A
  text-returning function whose value happens to look like an array would
  be spliced rather than quoted.
- `string_to_array` is not implemented (separate gap).
