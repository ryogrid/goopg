# pg_index.indexprs / indpred rendering (node-tree columns)

Status: accepted
Date: 2026-07-10
Milestone: unimplemented_feat #135 (pg_get_expr) follow-up

## Problem

`pg_index.indexprs` and `pg_index.indpred` are `pg_node_tree` columns. In real
PostgreSQL they store the serialized expression-tree for an expression index's
key expressions and a partial index's `WHERE` predicate, respectively, and are
**NULL** when the index is neither. `pg_get_expr(tree, relid)` decompiles the
node tree back to SQL text; tools also use `indpred IS NOT NULL` as the
canonical "is this a partial index?" probe.

goopg does not serialize node trees — it stores the already-deparsed SQL text
in every `pg_node_tree` column (`adbin`, `conbin`, `relpartbound`, and here
`indpred`). Its `pg_get_expr` is therefore a **pass-through** of that text,
which is correct *by construction* for every column that is populated with the
final SQL string.

The bug: the **live** SQL renderer for `pg_index`,
`catalog.InMemory.PGIndexRowsForDBOid` (`internal/catalog/catalog.go`),
hardcoded `indexprs`/`indpred`/`indcoloptions` to the empty string `""`. For a
`text` column, `""` reads back as a **non-NULL empty string**, not SQL NULL
(NULL requires the `catalog.VirtualNull` sentinel, which
`planner.TypedVirtualCell` maps to a NULL constant). Consequences on a live
query:

- `SELECT indpred FROM pg_index` returned `''` for every index — so
  `indpred IS NOT NULL` matched **all** indexes, mis-identifying every plain
  index as partial.
- `pg_get_expr(indpred, indrelid)` returned `''` instead of the `WHERE`
  predicate on a partial index, and `''` instead of NULL on a plain one.

## Three sibling renderers

`pg_index` rows are produced by three independent code paths that must agree
([[pattern_sibling_paths_must_agree]]):

| path | function | file | node-tree columns |
|------|----------|------|-------------------|
| live SQL (`SELECT … FROM pg_index`) | `PGIndexRowsForDBOid` | `internal/catalog/catalog.go` | **was `""` (bug)** |
| on-disk heap / PG-standby direct scan | `buildUserPGIndexRow` | `internal/executor/pg18_user_catalog_rows.go` | already correct (NULL / `PredicateString`) |
| initdb nailed-index bootstrap | `pgIndexRow` | `internal/initdb/initdb.go` | NULL (no user partial/expr indexes exist at bootstrap) |

The heap twin was already right — it emits `NullDatum` for a non-partial
`indpred` and `NewStringDatum(idx.PredicateString)` for a partial one. The live
renderer had silently drifted, so the same `SELECT indpred` gave different
answers depending on whether it was served from virtual rows (live) or a heap
scan (standby).

## Fix

`PGIndexRowsForDBOid` now mirrors the heap twin exactly:

- `indpred` = `idx.PredicateString` when `idx.HasPredicate`, else
  `catalog.VirtualNull`.
- `indexprs` = `catalog.VirtualNull` (no expression-index support in this path;
  `Index.Columns` holds only plain column names).
- `indcoloptions` = `catalog.VirtualNull`.

The synthetic TOAST-index rows (never partial/expression) emit `VirtualNull`
for all three as well.

`pg_get_expr` itself is unchanged: its pass-through is correct now that the
column carries either the real predicate text or SQL NULL.

## Tests

`internal/executor/pg_index_indpred_test.go`:

- `TestPgIndexIndpredPartialVsPlain` — end-to-end through `pg_get_expr`: a
  partial index's `indpred` and `pg_get_expr(indpred, indrelid)` both return
  the `WHERE` predicate text; a plain index's both return SQL NULL.
- `TestPgIndexRowsIndprIndexprsNullSentinel` — direct row-cell guard so a
  refactor cannot revert the `VirtualNull` sentinel back to a bare `""`.

## Remaining gap (deferred)

Expression-index `indexprs` is still never populated: `catalog.Index.ColExprs`
(the parsed key-expression targets of `CREATE INDEX … ((expr)))`) is not
surfaced into `pg_index.indexprs`, so `pg_get_expr(indexprs, indrelid)` on an
expression index returns NULL rather than the deparsed expression. psql `\d`
and `pg_dump` are unaffected — both reconstruct index DDL via
`pg_get_indexdef` / `buildIndexDefString` directly from index metadata, never
from `indexprs`. Resume point recorded in `.ralph/deferral_ledger.md`
(2026-07-10, unimplemented_feat #135): deparse `idx.ColExprs` into the
`indexprs` cell in **both** `PGIndexRowsForDBOid` and `buildUserPGIndexRow`.
