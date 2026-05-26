# 0097-0036 — CREATE TEMP VIEW/SEQUENCE parser dispatch

**Status:** accepted
**Milestone:** M0097-0036 (functional_deps regress porting)
**Date:** 2026-05-24

## Problem

`internal/parser/ddl.go` `parseCreate` handled `CREATE [GLOBAL|LOCAL]
TEMP[ORARY] …` by unconditionally consuming an optional `TABLE` keyword and
then calling `parseCreateTableTail`. As a result every temporary object kind
other than a table was mis-parsed:

- `CREATE TEMP VIEW fdv1 AS SELECT …` was fed to the table parser. No
  `*CreateViewStmt` was produced, so no view landed in the catalog. A later
  `DROP VIEW fdv1` then failed with `view "fdv1" does not exist`.
- `CREATE TEMP SEQUENCE …` and `CREATE TEMP MATERIALIZED VIEW …` hit the same
  fall-through.

In addition, `LOCAL` is a reserved keyword token (`KwLocal`), but the prefix
was only consumed via `acceptIdentKeyword("local")`, which matches
`TokenIdent` only — so `CREATE LOCAL TEMP …` failed outright with
`expected TABLE, INDEX, VIEW, … after CREATE (got local)`.

This surfaced in the `functional_deps` pg_regress case, which builds several
`CREATE TEMP VIEW` objects to exercise functional-dependency and dependency
tracking.

## Fix

After consuming the `TEMP`/`TEMPORARY` prefix, dispatch on the following
object keyword instead of assuming `TABLE`:

| Following token            | Parser tail                | Flag set        |
|----------------------------|----------------------------|-----------------|
| `VIEW`                     | `parseCreateViewTail`      | `Temporary=true`|
| `MATERIALIZED [VIEW]`      | `parseCreateMatViewTail`   | —               |
| `SEQUENCE`                 | `parseCreateSequenceTail`  | `temp=true`     |
| (default — incl. `TABLE`)  | `parseCreateTableTail`     | `Temporary=true`|

`CreateViewStmt` gains a `Temporary bool` field (parallel to
`CreateTableStmt.Temporary` / `CreateSequenceStmt.Temporary`). The executor's
`execCreateView` is unchanged: goopg views are virtual (no relation file) and
the regress cases are single-session, so the temporary flag is recorded but
not yet acted on. The `GLOBAL`/`LOCAL` prefix line now also accepts the
`KwLocal` keyword token.

`OrReplace` ordering is preserved: PostgreSQL grammar is
`CREATE [OR REPLACE] [TEMP|TEMPORARY] … VIEW`, and `orReplace` is already
parsed before the prefix, so it threads into `parseCreateViewTail`.

## Verification

- Unit: `TestParseCreateTempView` (TEMP/TEMPORARY/LOCAL TEMP forms →
  `*CreateViewStmt`, `Temporary=true`), `TestParseCreateTempSequence`
  (`CreateSequenceStmt.Temporary=true`). `internal/parser` suite green.
- End-to-end: `functional_deps` regress case normalized diff 24 → 21 lines.
  Views are now created (`DROP VIEW` no longer reports "does not exist"; the
  second `CREATE TEMP VIEW fdv1` now correctly reports `relation "fdv1"
  already exists`).

## Out of scope (remaining functional_deps diff, 21 lines)

1. **View-body validation at CREATE time.** goopg does not plan/validate the
   view's SELECT when the view is created, so an invalid `GROUP BY body` view
   is accepted instead of being rejected with the GROUP BY error. This is why
   the second `fdv1` create now hits `already exists`.
2. **`ALTER TABLE … DROP CONSTRAINT … RESTRICT` dependency tracking.** Needs
   pg_depend-style object dependency tracking (views depending on the PK/UNIQUE
   constraint used in their functional-dependency proof) to emit the
   `cannot drop constraint … because other objects depend on it` ERROR +
   DETAIL + HINT. This is a separate, larger feature.
