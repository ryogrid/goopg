# M0134-0189 — `xmlmap.sql`: sizing + a shared timestamptz layout fix

Status: **contained fix shipped, case stays `failed`** (2026-09-01). Sized
live for the first time; one independent bug in the shared timestamp-input
layout table fixed (removes the only non-XML error in the file). Full pass
blocked entirely by the SQL/XML publishing-function gap this file was
already known to need (M0134-0188's ledger row named it as blocked).

## What the file tests

`postgres/src/test/regress/sql/xmlmap.sql` exercises PostgreSQL's SQL/XML
*publishing functions* — turning relational data into XML/XSD, not parsing
XML: `table_to_xml`/`table_to_xmlschema`/`table_to_xml_and_xmlschema`,
`query_to_xml`/`query_to_xmlschema`/`query_to_xml_and_xmlschema`,
`cursor_to_xml`/`cursor_to_xmlschema`, `schema_to_xml`/`schema_to_xmlschema`/
`schema_to_xml_and_xmlschema`, and the `XMLFOREST(...)` expression form. It
also checks that a DOMAIN over a base type (`testboolxmldomain AS bool`,
`testdatexmldomain AS date`) publishes identically to its base type.

## Sizing (this loop, 2026-09-01)

`scripts/pg-regress-runner.sh -v xmlmap`: **0/1 PASS** throughout — the
timestamptz fix below removed the file's only non-XML-function error but
did not flip any statement to PASS, since every remaining statement in the
file calls one of the twelve unimplemented XML publishing functions.

Before any fix: 1340 diff lines, 13 distinct `^+ERROR` shapes (12 XML
function/grammar errors + the timestamptz one below). After the fix: 1338
diff lines, 12 distinct `^+ERROR` shapes (the timestamptz shape is gone;
every remaining line is one of the twelve XML-function calls).

## Fix landed: timestamptz literal accepts a space before the zone offset

`xmlmap.sql`'s own setup INSERT carries a timestamptz value with a space
before the numeric offset:

```sql
INSERT INTO testxmlschema.test2 VALUES (55, 'abc', 'def', 98.6, 2, 999, 0,
    '21:07', '21:11 +05', '2009-06-08 21:07:30', '2009-06-08 21:07:30 -07',
    '2009-06-08', NULL, 'ABC', true, 'XYZ');
```

goopg raised `22007 invalid input syntax for type timestamp: "2009-06-08
21:07:30 -07"` on the `rtz timestamptz` value — reproduced independently of
XML entirely:

```sql
SELECT '2009-06-08 21:07:30 -07'::timestamptz;  -- 22007 on goopg
SELECT '2009-06-08 21:07:30-07'::timestamptz;   -- (no space) worked fine
```

Verified against a local PG 18.3 cluster: PG accepts both spellings and
reads them as the same instant. This is not a special case — PostgreSQL's
`DecodeDateTime` (`postgres/src/backend/utils/adt/datetime.c`) tokenizes
input on whitespace as an ordinary field break, so a zone offset is legal
whether or not it touches the preceding time field. This is the same rule
`TestParseCopyTimestampISO8601SeparatorAndZulu`'s `canonicalZulu` already
applies to the `Z` spelling of UTC (`'10:00:00 Z'` == `'10:00:00Z'`) — it
had just never been extended to a *numeric* offset.

goopg reads timestamp(tz) text against a fixed table of Go `time.Parse`
layouts, `pgTimestampLayouts` (`internal/executor/copy_text.go`), unified
across every call site by M0119-0006. Go's `time.Parse` matches a layout
literally — a layout string with no space between the seconds field and the
zone element (`"2006-01-02 15:04:05Z07:00"`) will never match input with a
space in that position, and the table had no space-bearing variant for any
of its six zone-carrying entries.

Fix: added six space-variant entries (mirroring the existing six, for the
`HH`, `HHMM` and `HH:MM` offset widths on both the `' '` and `'T'`
date/time separators) plus a `"2006-01-02 15:04 Z07"` seconds-less form,
right after their no-space counterparts so the common (no-space) path still
matches first and costs nothing extra. Because `pgTimestampLayouts` already
backs both the CAST/typed-literal path (`evalTypedStringLit` →
`parsePGTimestampTextZoneSession`) and the COPY-TEXT/value-encoder path
(`parseCopyTimestampZoneSession`) — the same unification M0119-0006 did to
stop the two tables drifting apart — this one table edit fixed both sibling
paths at once; no second call site needed a matching change.

An alternative fix considered and rejected: generalizing `canonicalZulu` (in
`internal/utils/adt/datetime/normalize.go`) to also strip a space before a
numeric offset, the same way it folds the `Z` spelling. That would cover
every current and future layout with one rule instead of enumerating
variants, and matches the file's own stated philosophy ("folds the
case/spacing variants upstream in `pgdatetime.NormalizeInput`" —
`timestamp_iso8601_tz_input_test.go`). It was set aside for this loop in
favor of the lower-risk, already-verified layout-table addition — a
regex/scan-based offset detector risks its own edge cases (trailing
`AM`/`PM`, zone abbreviations, the leap-second/hour-24 canonicalization this
same function composes with) that the existing test suite does not
exercise yet. Left as a possible follow-up if another zone-spacing gap
surfaces; not itself a functional gap since the layout-table fix is
complete for every spelling PG's tokenizer accepts here.

New test: `TestParseCopyTimestampSpaceBeforeOffset`
(`internal/executor/timestamp_iso8601_tz_input_test.go`) — pins the four
offset widths with a space, confirms the no-space spelling still works
(mutation guard against a layout-table-only regression), and confirms
`timestamp` (without time zone) still discards the offset per
`tsZoneModeForType`'s existing rule regardless of the space.

## Remaining gap: the SQL/XML publishing-function family (unattempted)

Every other statement in the file fails because the target function has no
executor implementation at all. Full detail and resume points are in the
`.ralph/deferral_ledger.md` M0134-0189 row; summary:

- `table_to_xml`, `table_to_xmlschema`, `table_to_xml_and_xmlschema`,
  `query_to_xml`, `query_to_xmlschema`, `query_to_xml_and_xmlschema`,
  `cursor_to_xml`, `cursor_to_xmlschema`, `schema_to_xml`,
  `schema_to_xmlschema`, `schema_to_xml_and_xmlschema` are registered in
  `pg_proc` (`internal/catalog/pg_proc_names_generated.go`) and the
  non-immutable-builtins list (`internal/executor/
  pg_nonimmutable_builtins.go`) but have no builtin dispatch entry — every
  call raises `function ... does not exist`. The upstream reference
  (`postgres/src/backend/utils/adt/xml.c:2867-3465`, ~600 lines) is a real
  subsystem: row-to-XML serialization plus a full SQL-type-to-XML-Schema
  mapper, not a thin wrapper — REFACTOR-tier.
- `xmlforest(c1, c2, c3, c4)` is not a function call at all; it is its own
  `gram.y` grammar production (`XMLFOREST '(' xml_attribute_list ')'`), the
  same SQL/XML publishing-clause grammar gap already filed as M0134-0188's
  gap (a), which already named this file as blocked by it.

Neither piece fits this loop's budget; both are independently REFACTOR-tier
and share no code with the timestamptz fix above.

## Gates run

`go build ./...` clean; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (full unit suite incl.
`internal/initdb`); `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=34); the
pre-commit pgbench smoke gate runs via the git hook at commit time.
