# M0134-0188 — `xml.sql`: `xml` type well-formedness validation

Status: **contained fix shipped, case stays `failed`** (2026-09-01). Sized
live for the first time; a real correctness bug fixed (silent acceptance of
malformed XML) across both the explicit-cast and implicit column-coercion
paths. The dominant remainder is two REFACTOR-tier subsystems (SQL/XML
publishing-function grammar, and XPath evaluation) — see below.

## What the file tests

`postgres/src/test/regress/sql/xml.sql` (681 lines) exercises PostgreSQL's
`xml` data type end to end: input well-formedness (DOCUMENT vs CONTENT per
the `xmloption` GUC), the SQL/XML publishing functions (`XMLELEMENT`,
`XMLATTRIBUTES`, `XMLFOREST`, `XMLCONCAT`, `XMLPI`, `XMLROOT`, `XMLPARSE`,
`XMLSERIALIZE`, `XMLTABLE`, `XMLEXISTS`), plain functions (`xmlcomment`,
`xmltext`, `xml_is_well_formed[_document|_content]`, `xpath`,
`xpath_exists`), and `xmlbinary`/`xmloption` GUC-driven serialization.

## Sizing (this loop, 2026-09-01)

`scripts/pg-regress-runner.sh -v xml`: **0/1 PASS**. Before any fix: 2222
diff lines, 239 `^+ERROR` shapes, 38 `^-ERROR` shapes. After the fix below:
**2202 diff lines, 239 `^+ERROR`, 37 `^-ERROR`**.

The `^+ERROR` count is unchanged because the fix's effect is mostly
*replacing* one divergence with another smaller one (a wrongly-succeeded
statement becomes a correctly-raised error whose DETAIL text still doesn't
match libxml2's, so the line remains a diff line) rather than eliminating
lines outright — see "Why the line-count win is small" below.

## Root cause and fix

goopg had **no `evalCast` arm for `"xml"` at all**: an `xml`-typed value was
opaque, unvalidated text, all the way from `xml_in`'s two upstream call
paths (an explicit `::xml` cast and an implicit column-INSERT/UPDATE
coercion). This is the fifth instance of the recurring "missing evalCast arm
= unvalidated text" pattern (xid, circle, float8, and range types were the
first four — see `internal/executor/rangetypes.go`'s header comment).
Concretely, before this fix:

```sql
INSERT INTO xmltest VALUES (3, '<wrong');   -- PG: ERROR 2200N invalid XML content
SELECT '<wrong'::xml;                       -- PG: ERROR 2200N invalid XML content
SELECT pg_input_is_valid('<value>one</', 'xml');  -- PG: f
```

all three **succeeded** on goopg and stored/reported the malformed fragment
verbatim.

Unlike the earlier four instances, this one needed a fix on **two** sibling
paths, not one, because goopg carries two independent codecs for a
`KindString` Datum flowing into a typed column:

1. **`evalCast`** (`internal/executor/expr.go`) — the explicit `::xml` cast
   and the sole entry point `pg_input_is_valid`/`pg_input_error_info` also
   call into (via the new shared `xmlValidate` helper).
2. **`encodeValuePGCtx`** (`internal/executor/codec.go`) — the physical
   heap-row encoder that INSERT/UPDATE actually calls for column values;
   it never routes through `evalCast` for an *implicit* coercion (no
   explicit `CastExpr` node is inserted for an untyped literal reaching an
   `xml` column), so the `INSERT INTO xmltest VALUES (3, '<wrong')` case
   above needed its own fix at this second choke point. Falling through to
   the shared `default:` arm (`coerceTextLikeDatum`, which has no `ctx` and
   thus no access to the session `xmloption` GUC) would have silently kept
   accepting malformed content, so `encodeValuePGCtx` gained its own `"xml"`
   case that reuses `coerceTextLikeDatum` for the Datum→string extraction
   step and then applies the same `xmlValidate` gate before writing the
   varlena bytes.

This is the *sibling paths must agree* pattern (`pattern_sibling_paths_must_
agree`) recurring at a finer grain than usual: it is not evalCast vs. a
different evaluator, but evalCast vs. the physical row-encode path — the
same duality `macaddr8`/`jsonb`/`varchar` already navigate via
`coerceTextLikeDatum`.

### `xmlValidate` (new file `internal/executor/xmltypes.go`)

A well-formedness *check*, not an XML engine: it answers the same yes/no
question `xml_parse`'s DOCUMENT/CONTENT branch answers (exactly one root
element required for DOCUMENT, none required for CONTENT — content is a
"general parsed entity" fragment per the XML spec) using Go's standard
library `encoding/xml.Decoder` in strict mode instead of libxml2. It
tokenizes the whole input, tracks element nesting depth and non-whitespace
character data at depth 0, and reports `2200N`/`invalid XML content` or
`2200M`/`invalid XML document` on failure. It does **not** reproduce
libxml2's DETAIL diagnostics (line/column, "Couldn't find end of Start Tag
…") — only the ERRCODE and top-level message PG's `xml_parse` raises
(`postgres/src/backend/utils/adt/xml.c:1873-1888`).

The session `xmloption` GUC (already declared, `internal/utils/misc/
defaults.go:1409`, boot value `content` — previously **unconsumed**,
matching the recurring `goopg_declared_but_unconsumed_gucs` pattern) is now
read via `ctx.GetSetting("xmloption")`, mirroring `timeZoneFromCtx`'s
dispatch (`xmlOptionFromCtx`).

### Sibling wiring

- `evalCast`'s new `"xml"` case (`expr.go`).
- `pg_input_is_valid`'s per-type switch gained a `"xml"` case (`expr.go`).
- `pg_input_error_info`'s per-type switch gained a `"xml"` case
  (`operators_pg_input_error_info.go`).
- `encodeValuePGCtx`'s new `"xml"` case (`codec.go`), for implicit
  column-coercion INSERT/UPDATE.

## Why the line-count win is small

Most of the 2202 remaining diff lines belong to two subsystems this fix does
not touch:

- **SQL/XML publishing-function grammar** (`XMLELEMENT`, `XMLFOREST`,
  `XMLCONCAT`, `XMLPI`, `XMLROOT`, `XMLPARSE`, `XMLSERIALIZE`, `XMLTABLE`,
  `XMLEXISTS`, `XMLNAMESPACES`, `XMLATTRIBUTES`). These are **not** ordinary
  function calls — `postgres/src/backend/parser/gram.y:16040-16110` and
  `:14133-14270` give each one its own grammar production
  (`makeXmlExpr`/`RangeFunction`). goopg's grammar (`grammar/*.y`) carries
  the keywords (`XMLCONCAT` etc. are already reserved, `grammar/tokens_gen.y:
  84-85`) but has **no production at all** for any of them — every one is a
  syntax error. This is the same shape as the already-filed SQL/JSON
  constructor gap (M0134-0168a) and is REFACTOR-tier: a whole new expression
  grammar family plus its executor-side node.
- **XPath evaluation** (`xpath`, `xpath_exists`) and the other plain XML
  functions (`xmlcomment`, `xmltext`, `xml_is_well_formed[_document|
  _content]`, `xmlconcat` as a variadic function) are missing entirely
  (`function … does not exist`). `xmlcomment`/`xmltext`/`xml_is_well_
  formed*` are implementable leaf functions (no XPath needed) and are the
  natural next contained slice; `xpath`/`xpath_exists` need a real XPath 1.0
  evaluator, which is its own scoped subsystem.

The well-formedness fix itself is real and load-bearing (it is a
data-integrity fix, not a message-wording one — see the range-types
precedent, M0134-0173), but very few individual `xml.sql` assertions were
*solely* blocked by it; most statements that newly raise the correct error
still mismatch on DETAIL text, and most of the file's other statements are
blocked by one of the two gaps above regardless.

## Tests

- `internal/executor/xmltypes_test.go` (new):
  `TestXMLWellFormedness` — `evalCast` content-mode acceptance/rejection
  table (well-formed element, self-closing, unterminated start/end tag,
  mismatched tags, plain-text fragment), a DOCUMENT-vs-CONTENT multiple-root
  case (`xmlValidate` directly), and a well-formed-value round-trip check.

## Gates run

- `go build ./...` clean.
- `go test ./internal/optimizer/... ./internal/parser/... ./internal/parser/analyzer/... ./internal/catalog/... ./internal/executor/...` — all PASS.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — PASS (full
  suite, including `internal/initdb` and `cmd/goopg`).
- `scripts/tpch-spotcheck.sh` — PASS (Q12=2 rows, Q13=34 rows).
- `scripts/pg-regress-runner.sh -v xml` — 0/1 PASS (expected; case stays out
  of the pass-required set), 2202 diff lines / 239 `^+ERROR` / 37 `^-ERROR`
  (down from 2222 / 239 / 38).
- Regression A/B: stashed this loop's `codec.go`/`expr.go`/
  `operators_pg_input_error_info.go`/`xmltypes.go` changes and re-ran
  `create_table`, `alter_table`, `type_sanity` (the three regress files most
  likely to touch the shared `encodeValuePGCtx`/`evalCast` switches) —
  diffs are byte-identical before and after (only diff-header timestamps
  differ), zero regressions.
- Live manual check (throwaway server, cgroup-capped): `CREATE TABLE
  xmltest(id int, data xml); INSERT INTO xmltest VALUES (3, '<wrong');` now
  raises `ERROR: invalid XML content` instead of silently storing the
  fragment; `SELECT '<wrong'::xml` likewise.

## Remaining gaps (deferral ledger, M0134-0188 row, 2026-09-01)

- **(a) SQL/XML publishing-function grammar** (`XMLELEMENT`, `XMLTABLE`, …)
  — REFACTOR-tier, ~150+ syntax errors across the file, own milestone (grammar
  + executor node), read `docs/design/not_ralph/06-goyacc-parser-playbook.md`
  §12 before touching `grammar/*.y`.
- **(b) plain XML functions and XPath** (`xmlcomment`, `xmltext`,
  `xml_is_well_formed[_document|_content]`, `xmlconcat`, `xpath`,
  `xpath_exists`) — `xmlcomment`/`xmltext`/`xml_is_well_formed*` are
  contained leaf-function additions (no XPath needed); `xpath`/
  `xpath_exists` need a real XPath 1.0 evaluator.
- **(c) `xmlbinary`/`xmloption` as settable GUCs via `SET XML OPTION
  DOCUMENT|CONTENT`** — `xmloption` is now *read*, but `SET XML OPTION …`
  is its own special grammar form (`gram.y` `VariableSetStmt` XML clause,
  distinct from a plain `SET xmloption = …`), currently unparsed
  (`unrecognized configuration parameter "XML"` / `"xmlbinary"`).
- **(d) declaration-level well-formedness checks not modeled by
  `encoding/xml`** — e.g. `standalone="y"` (only `yes`/`no` are legal XML
  declaration values) is accepted by Go's decoder but PG's libxml2-backed
  parser rejects it; `xmlValidate` is a syntax/structure check, not a full
  XML 1.0 conformance validator.

Resume points and upstream citations for each are in the ledger row itself.

## Next case

Per `.ralph/fix_plan.md` M0134 ordering: **M0134-0189** (`xmlmap.sql`,
`not-tried`) — note it is gated by the same bucket (a) grammar gap and (b)
function/XPath gap, so expect the same sizing-and-park shape unless one of
those subsystems is opened first.
