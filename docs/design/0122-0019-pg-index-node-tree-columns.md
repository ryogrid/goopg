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

## Follow-up (2026-07-10): live-path `indexprs` for expression indexes

The prior "remaining gap" (below) is now closed for the **live** query path.
`pg_get_expr(indexprs, indrelid)` on an expression index returns the deparsed
expression text instead of NULL.

New shared helper `catalog.IndexExprsText(idx *Index) (string, bool)`
(`internal/catalog/catalog.go`) is the single source of truth. It walks
`idx.Columns`, and for each **expression** key column (`Columns[i]==""`, the
ordinal-0 entries in `indkey`) appends `idx.ColExprStrings[i]` — the natural
deparse produced by `defaultExprToSQL`, the same serialization
`buildIndexDefString` consumes. The elements are joined **verbatim** with
`", "`; it returns `("", false)` when the index has no expression columns, so
`PGIndexRowsForDBOid` emits `VirtualNull` (SQL NULL) — matching real PG, where
`indexprs IS NULL` for a non-expression index.

Output was byte-matched against PostgreSQL 18.3:

| index | `pg_get_expr(indexprs)` |
|-------|-------------------------|
| `(lower(b))` | `lower(b)` |
| `((a+c), upper(b))` | `(a + c), upper(b)` |
| `(a, (a*c))` | `(a * c)` |
| `(a)` | NULL |

The parens come from the per-expression natural deparse already stored in
`ColExprStrings` (a binary/arithmetic expression keeps its wrapping parens, a
bare function call has none). An earlier draft that reused
`buildIndexDefString`'s `indexKeyIsBareFuncCall` rule to add parens on top
double-wrapped binexprs into `((a + c))`; the helper joins verbatim instead.

### Heap-persisted twin stays NULL — deliberate

`buildUserPGIndexRow` (`internal/executor/pg18_user_catalog_rows.go`) still
writes `indexprs=NULL` in the heap row, so the live and heap renderings diverge
here **on purpose**. `DecodePGIndexPhysicalRow` (`internal/catalog/codec.go`)
infers `indpred`'s presence from the bytes remaining after `indoption`, which is
only unambiguous while `indexprs` (the immediately-preceding nullable varlena,
ordinal 19) is NULL — two consecutive nullable varlenas are indistinguishable
from the data bytes alone, and the decoder receives only the tuple data portion,
not its null bitmap. Writing a non-NULL `indexprs` to the heap would corrupt an
expression index's `indpred` on a checkpointed restart. Closing this needs a
null-bitmap-aware decoder; the resume point is recorded in
`.ralph/deferral_ledger.md` (2026-07-10, unimplemented_feat #135, indexprs
slice).

### Tests

`internal/executor/pg_index_indexprs_test.go`:

- `TestPgIndexIndexprsExpressionIndex` — end-to-end through `pg_get_expr` with
  the PG-18.3-captured expectations above.
- `TestIndexExprsTextParenAndNullRules` — direct unit test on
  `catalog.IndexExprsText` guarding the no-double-paren and NULL-vs-present
  contract.

## Remaining gap (deferred) — historical, live path now closed above

Expression-index `indexprs` was never populated: `catalog.Index.ColExprs`
(the parsed key-expression targets of `CREATE INDEX … ((expr)))`) was not
surfaced into `pg_index.indexprs`, so `pg_get_expr(indexprs, indrelid)` on an
expression index returned NULL rather than the deparsed expression. psql `\d`
and `pg_dump` are unaffected — both reconstruct index DDL via
`pg_get_indexdef` / `buildIndexDefString` directly from index metadata, never
from `indexprs`. The live path is now fixed (see Follow-up above); the
heap-persisted path remains deferred (decode-ambiguity landmine).
