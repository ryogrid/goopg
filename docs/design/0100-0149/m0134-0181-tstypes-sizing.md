# M0134-0181 — `tstypes.sql`: sizing (PARKED, new milestone M0136 filed)

Status: **PARKED** (case sized live for the first time; no contained fix
found — the whole file traces to one absent subsystem, not a handful of
independent gaps like M0134-0180's `tsrf.sql`).

## What the file tests

`postgres/src/test/regress/sql/tstypes.sql` (282 lines) exercises the
`tsvector`/`tsquery` **core type system itself** — literal I/O (parsing and
canonical output of the `'lexeme:weight,position ...'` grammar), comparison
operators, the `@@` match operator and `<->` phrase-distance operator,
`ts_rank`/`ts_rank_cd` scoring, and a family of "editing" functions
(`strip`, `setweight`, `ts_delete`, `ts_filter`, `numnode`, `tsquery_phrase`,
`tsvector_to_array`, `array_to_tsvector`). It is deliberately narrower than
`tsdicts.sql`/`tsearch.sql` (M0134-0178/0179, both PARKED on the absent
*dictionary/stemmer* engine): most of this file's assertions construct
`tsvector`/`tsquery` values directly from literals (`'a:1 b:2'::tsvector`)
rather than via `to_tsvector('english', text)`, so they do **not** need a
tokenizer, stemmer, or `pg_ts_dict`/`pg_ts_config` lookup — only the core
type kernel and its operators.

## Sizing (this loop, 2026-09-01)

`scripts/pg-regress-runner.sh -v tstypes`: **0/1 PASS, 1839 diff lines, 159
`^+ERROR`, 4 `^-ERROR`** (first live run — CSV was `not-tried`).

### Root-cause bucketing (confirmed via live repro, not diff inspection alone)

1. **`tsvector`/`tsquery` cast is a naive text passthrough — no real parser
   exists.** `grep`ing `internal/` for any `TSVector`/`tsquery` parsing type
   or `func.*[Tt]svector` found nothing outside catalog OID/name plumbing
   (`internal/executor/expr.go:140-142,20038-20046` — type-name formatting
   only; `internal/catalog/codec.go:1707-1709,1902-1904` — same). `SELECT
   '1'::tsvector` "succeeds" only because goopg falls back to storing/
   echoing the input string verbatim for a type with no registered I/O
   function, not because a `tsvectorin`-equivalent exists. This explains the
   diff's early lines precisely: PG canonicalizes a bare lexeme with quotes
   (`'1'`) and escapes embedded quotes (`'1 ''2'`); goopg echoes the raw
   input unchanged (`1`, `'1 \'2'`). Calling `tsvectorout(...)` explicitly
   (rather than relying on the implicit cast-to-text-for-display path)
   errors `function tsvectorout does not exist` — confirming there is no
   real function backing the type, just an opaque-type display fallback.
2. **`@@` match operator: `unsupported operator "@@"`, ~89 occurrences** —
   the single largest bucket. No tsvector/tsquery matching logic exists at
   all (`ts_match_tq`/`ts_match_vq`/`ts_match_qv`/`ts_match_tt`,
   `postgres/src/backend/utils/adt/tsvector_op.c:2206-2310`).
3. **`<->` phrase-distance operator: `unsupported operator "<->"`, 4
   occurrences** — `TS_execute`'s phrase-distance mode
   (`tsvector_op.c` phrase matching, `ts_phrase_execute`); same absence.
4. **12 "function X does not exist" errors, 65 occurrences total**: `strip`
   (3), `setweight` (6), `ts_delete` (12), `ts_filter` (3), `numnode` (3),
   `tsquery_phrase` (1), `tsvector_to_array` (2), `array_to_tsvector` (4),
   `tsvectorout` (1), `ts_rank`/`ts_rank_cd` (9+20 — the single biggest
   named-function bucket), `to_tsvector` (1, the only call in this file that
   *would* need the dictionary engine). All of these operate on an
   already-parsed `tsvector`/`tsquery` value and are pure data-structure
   manipulation or scoring — see `postgres/src/backend/utils/adt/
   tsvector_op.c:168(strip),211(setweight),554/578(delete_str/arr),
   720(to_array),747(array_to_tsvector),819(filter)`,
   `tsquery_util.c`(numnode/tsquery_phrase), `tsrank.c:439-1010`
   (`ts_rank*`/`ts_rankcd*` — 4 arities each, weighted by cover density).
5. **2 `operator &&: invalid box value` errors** — unrelated pre-existing
   geometry-type coercion noise from a `box`-typed sibling assertion caught
   in the same diff window; not a tsvector/tsquery issue, out of scope here.

### What this means for scoping

Unlike `tsrf.sql` (M0134-0180, ~10 independent placement rules) or
`tsdicts.sql`/`tsearch.sql` (M0134-0178/0179, one absent dictionary engine
with a small contained parser bug alongside it), `tstypes.sql` is dominated
by **one missing subsystem with no small independent slice inside it**: the
`tsvector`/`tsquery` type kernel (parse, canonical format, compare) plus its
operators (`@@`, `<->`) and scoring functions (`ts_rank*`). There is no
narrow bounded fix available — even the most contained-looking item
(`tsvectorout` quoting) cannot be fixed in isolation, because the "cast"
that currently appears to work is not backed by a real parser at all; fixing
output formatting alone would just print differently-wrong text for values
that were never structurally parsed, not move any assertion to a real pass.

This is REFACTOR-tier, filed as its own milestone: **M0136** (see
`docs/design/README.md` and `.ralph/fix_plan.md`). It is a genuine
*prerequisite* for parts of the already-parked M0134-0178/0179/0180
dictionary-engine work (`to_tsvector`'s *result* is a `tsvector` value that
still needs the same canonical-output/compare machinery this milestone
builds), though M0136 itself needs none of the dictionary/stemmer machinery
— it is strictly narrower and more foundational than ledger row 0178a.

## Resume points

- New milestone **M0136** (`.ralph/fix_plan.md`, filed this loop) — S1 (type
  kernel: parser + canonical pretty-printer for `tsvectorin`/`tsvectorout`/
  `tsqueryin`/`tsqueryout`, oracle `postgres/src/backend/utils/adt/
  tsvector.c`/`tsvector_parser.c`/`tsquery.c`), S2 (editing/utility
  functions: `strip`/`setweight`/`ts_delete`/`ts_filter`/`numnode`/
  `tsquery_phrase`/`tsvector_to_array`/`array_to_tsvector`, oracle
  `tsvector_op.c`/`tsquery_util.c`), S3 (`@@` match + `<->` phrase-distance
  operators, oracle `tsvector_op.c:2206-2310` + phrase-execute path), S4
  (`ts_rank`/`ts_rank_cd`, oracle `tsrank.c:439-1010`).
- Re-arm M0134-0181 once M0136-S1 lands — re-measure `tstypes` live; S1 alone
  should clear bucket 1 (~40+ diff lines of pure formatting divergence) even
  before S2-S4 land.

## Gates run

- `scripts/pg-regress-runner.sh -v tstypes` (sizing run; no code changed this
  loop, so no build/unit gates were needed beyond the runner itself
  completing cleanly).
- `make ralph-state-guard` PASS.
