# Array elements render under the session DateStyle/TimeZone (M0119-0006)

- status: accepted
- date: 2026-08-12
- supersedes: nothing; closes the deferral-ledger row of 2026-08-12
  ("`timestamptz` array elements are rendered in UTC unconditionally")
- related: `0119-0006-array-element-datetime-images.md` (the stored images these
  render), `0119-0006-timestamptz-output-zone-rendering.md` (the SCALAR path,
  whose formatters this reuses), `0119-0006-timestamptz-datum-origin-tags.md`

## The gap

`array_out` (`postgres/src/backend/utils/adt/arrayfuncs.c`) formats nothing
itself. It looks up the ELEMENT type's output function and calls it once per
element, so a `timestamptz` inside an array goes through `timestamptz_out` and
honours the session `TimeZone` and `DateStyle` exactly as a scalar column does;
`date`/`timestamp` elements honour `DateStyle`. Measured on PG 18.3:

```
SET TimeZone='America/Los_Angeles'; SET DateStyle='Postgres, MDY';
SELECT ARRAY['2020-06-15 10:00:00+00','2020-01-15 10:00:00+00']::timestamptz[];
 → {"Mon Jun 15 03:00:00 2020 PDT","Wed Jan 15 02:00:00 2020 PST"}
```

goopg rendered every date-time array element in ISO/UTC unconditionally
(`pgdatetime.FormatTimestampTZUTC`, `FormatTimestamp`, `FormatDate`). The
`timestamptz` case is the one the ledger row was filed against and is the
damaging one: the stored instant is right, so nothing errors, but a session in
any other zone reads the value back under the wrong offset — the same silent
five-and-a-half-hour class of error the scalar path fixed in the 39th slice.
`date`/`timestamp` elements were a second, unfiled divergence found while
measuring: they ignored `DateStyle` too.

`time` and `timetz` elements are correctly style-independent — confirmed against
the oracle, not assumed: `SET DateStyle='German'` leaves a `time[]` element as
`10:00:00`.

## Why the blocker the ledger row recorded was the wrong one

The row named the blocker as "goopg has no tzdata-backed zone lookup in a leaf
package yet". That is no longer true — the 39th slice put `sessionLocation` (a
`time.LoadLocation` behind a `sync.Map`) and the full `EncodeTimezone` port in
`internal/config`, which is a **true leaf**: `go list -deps ./internal/config`
names no goopg package at all, and `internal/pgarray` already depends on it.

The real blocker is structural and was not recorded: **goopg renders an array to
its `{…}` text during the HEAP DECODE**, where upstream defers to `array_out` at
OUTPUT time. The scalar date-time types keep their `KindTime` carrier all the
way to `appendTypedCellText` / `datumToCopyText`, which is why the 39th slice
could read the GUCs there. An array arrives at those functions as a
`KindString` whose text is already fixed. And the decode entry point
(`DecodeRowIntoMctxPGTuple`) has ~70 call sites, most of which — catalog reload,
VACUUM, ANALYZE, DDL rescans — have **no session at all** and must not acquire a
dependency on one.

## What landed

An explicit `pgarray.OutputStyle{Style, Order, Zone}` parameter with a
documented default, threaded from the operators that hold a `*executor.Context`
and left at the default everywhere else. No ambient/global lookup: the sites
that have no session are the majority, and they are the ones that must stay
deterministic.

`internal/pgarray`:

- `OutputStyle` + `DefaultOutputStyle()` (ISO/MDY, UTC — byte-identical to the
  pre-slice behaviour, pinned by a test so the ~70 default callers cannot drift).
- `FormatDateElem` / `FormatTimestampElem` / `FormatTimestampTZElem` — the
  ±infinity sentinels (style-independent, per `EncodeSpecialTimestamp`) then a
  call into **`internal/config`'s own formatters**, i.e. the very functions the
  scalar output path calls. Element text and column text are the same text by
  construction, not by review.
- `RenderTextStyled` / `DecodeElemStyled`; the old `RenderText` / `DecodeElem`
  remain as default-style wrappers.

Integer→`time.Time` conversion avoids `time.Duration` in both directions on
purpose: `date` spans to 5874897 AD and `timestamp` to 294276 AD, and neither
day count nor microsecond count fits a nanosecond int64. `FormatDateElem` uses
`AddDate`; `timestampMicrosToTime` splits seconds from microseconds.

`internal/executor`:

- `arrayOutputStyle(ctx)` reads the same GUC spellings and boot defaults
  `RunCopyTo` reads (`copy.go`) — they are siblings.
- `decodeArrayValuePGStyled`, `decodePhysicalPGValueMctxStyled`,
  `DecodeRowIntoMctxPGTupleStyled`, `decodeIndexKeyColumnStyled`,
  `decodeBTreeKeyToDatumStyled`. Every plain entry point survives unchanged as a
  default-style wrapper, so the ~70 session-less call sites and the existing
  tests are untouched.
- `seqScanOp`, `bitmapHeapScanOp`, `indexOnlyScanOp` resolve the style **once in
  Open**, and the two heap scans do it only when `colsHaveArray(o.cols)` — an
  array-free relation (every TPC-H table) pays nothing.

### The sibling that had to move with it

An index-only scan rebuilds array text from the INDEX KEY
(`arrayKeyElemRendererPGImage`), a seq/bitmap scan from the HEAP. Threading the
GUCs into one and not the other would make the same row print differently
depending on which plan the planner picked — visible only under a non-default
session, which is precisely what no existing test covered. Both paths now call
the same `pgarray.Format*Elem` functions with the same style
(Hard-won Rule #2). `TestArrayKeyTextMatchesHeapTextUnderSessionStyle` pins it,
and a scripted revert confirms it goes red.

`internal/wal`'s pgoutput decoder keeps the default. That is not an omission:
upstream's logical decoding runs the type output functions under the
**walsender's** GUCs, not a user session's, and no session reaches that code.
Ledger row.

## Deliberately not in scope

`COPY … TO` of any non-text array column errors out entirely — `int4[]` gives
`expected int datum for int4, got kind 3`, `date[]` gives `expected time
datum for date, got kind 3`. This is **pre-existing at HEAD and unrelated to
this slice** (verified: the `int4[]` failure has no date-time content at all);
`datumToCopyText` dispatches on the element type name without consulting
`Type.IsArray`, so it takes the scalar arm and rejects the `KindString` array
datum. Ledger row with the resume point.

## Verification

`internal/pgarray/array_elem_output_style_test.go` — 33 cells captured live from
PostgreSQL 18.3, with the integer images taken from PG's own stored forms
(`(extract(epoch from …)*1000000)::bigint - 946684800000000`,
`<date> - '2000-01-01'::date`) so the test compares against the oracle rather
than a re-derivation of it: every DateStyle, three zones, DST moving both clock
and offset, an `HH:MM` offset (Kolkata), an `HH:MM:SS` Local Mean Time offset
(pre-1906 BC timestamps), the era marker trailing the ZONE, a zone whose tzdata
abbreviation is itself numeric (`+0545`), fractional seconds, and both
sentinels. The plain-`timestamp` cases assert the value does NOT move when
`TimeZone` changes.

`internal/executor/array_elem_output_style_test.go` — the end-to-end heap
encode→decode under four sessions, the heap↔index-key sibling guard, the GUC
resolver, and the `colsHaveArray` cost guard.

End-to-end on a live goopg (throwaway cluster, port 5533): `SELECT` of
`timestamptz[]`/`timestamp[]`/`date[]` under UTC / Asia/Kolkata /
America/Los_Angeles × ISO / Postgres — every cell matches the PG 18.3 oracle.

Gates: `internal/pgarray` + `internal/executor` + `internal/config` +
`internal/wal` suites, `TestPort_RegressSuite`, `RALPH_PRECOMMIT_SCOPE=units`,
`scripts/tpch-spotcheck.sh`.
