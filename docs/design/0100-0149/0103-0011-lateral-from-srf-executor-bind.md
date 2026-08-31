# 0103-0011: LATERAL FROM-clause SRF executor binding (rung 6 closure)

Status: accepted
Milestone: M0103-0008
Date: 2026-05-14

## Background

`docs/design/0103-0010-lateral-from-srf-arg-resolution.md` closed the
**planner** side of the libpqrcv `fetch_remote_table_info` column-list
probe (rung 6). The planner now threads a per-FROM-item LATERAL
`resolveContext` through `planScanRangeVar` /
`planTableFuncRangeVar` / `planPgGetPublicationTables` so a FROM-list
SRF whose argument references an outer sibling

```sql
SELECT gpt.attrs
  FROM pg_publication p,
       LATERAL pg_get_publication_tables(p.pubname) gpt
```

resolves `p.pubname` against the left FROM item at plan time and
produces the SRF's static `(relid oid, attrs text, qual text)` schema
on the right of a `JoinTypeCross`.

The 0103-0010 loop explicitly left the **executor** side for the next
rung: `joinOp` opened the right child once, with no outer slot bound,
so at `Next` time the SRF's `ColumnRef("pubname")` fell through to
`evalExprSlot`'s nil-slot guard:

```
XX000: column ref pubname/0 on nil slot
```

This slice closes the executor side.

## Change

### `internal/planner/plan.go`

`Join` gains a `Lateral bool` field. The planner sets it whenever the
right child of a FROM-list cross-join references the LATERAL outer
context.

### `internal/planner/planner.go`

Three sites that build a `*Join` over a FROM-clause sibling now
populate `Lateral`:

* `planFromRangeVars` (legacy comma-separated FROM)
* `planFromClause` (FromExpr/Join path)
* `planFromItem` (right side of an explicit JOIN)

The detection runs through a new helper

```go
func nodeReferencesOuter(n Node) bool
func exprContainsColumnRef(e Expr) bool
```

`nodeReferencesOuter` switches on the right-side node type. The only
positive case today is `*PgGetPublicationTables` — its `Args` are
walked with `walkExprTree`, and any `*ColumnRef` inside the arg list
trips the flag. Generic LATERAL subqueries / table funcs would extend
this helper with their own walker. Conservative by construction:
non-LATERAL right children retain the materialise-both-sides default.

### `internal/executor/operators_pg_get_publication_tables.go`

`pgGetPublicationTablesOp` gains an `outerSlot SlotView` field plus a
`BindLateralOuter(slot SlotView)` method. Open() now evaluates each
arg via `evalExprSlot(a, o.outerSlot, ctx)` instead of
`evalExpr(a, nil, ctx)` so a `*ColumnRef` resolves through the bound
outer row. `nil` outerSlot preserves the original
"args must be self-contained" semantics for the non-LATERAL FROM-clause
SRF entry.

### `internal/executor/operators_join_agg.go`

A new `lateralBindable` interface and `joinOp.openLateral` path drive
the per-outer-row execution:

1. Open the left and drain its rows.
2. Build a single reusable `*MaterializedSlot` over the left's schema
   and bind it on the right via `BindLateralOuter`.
3. For each left row: overwrite the bound slot's row, `Open` the
   right (so its arg evaluation sees the new outer row), drain right
   rows, evaluate the join predicate, and append concatenated rows
   to the existing `o.rows` buffer. `Close` the right between
   iterations to release any per-Open state.
4. LEFT join semantics: when the SRF returns zero rows for an outer,
   emit the null-padded outer row.

The Open dispatch order is:

```
Semi/Anti → openLazyHashJoin
Hash      → openLazyHashJoin
Lateral   → openLateral   ← NEW
default   → drain both + runMergeJoin / runNestedLoop
```

The lateral path materialises into the existing `o.rows` slice so the
existing `Next()` emit loop does not need to change.

### Tests

* `internal/executor/operators_pg_get_publication_tables_test.go`
  - `TestLateralPgGetPublicationTablesFromOuterRef` — two outer rows
    (`p`, `q`) each yield one SRF result row; pins the per-outer-row
    binding and arg re-evaluation.
  - `TestLateralPgGetPublicationTablesUnknownYieldsZero` — an outer
    row whose `pubname` does not match any registered publication
    drops out of the CROSS-join shape (zero SRF rows ⇒ outer row not
    emitted).

* `internal/planner/planner_test.go::TestPlanFetchTableListAggDerivedSubquery`
  — added as `t.Skip` to pin the **next** rung exactly (rung 7 below).

## What remains for full `TestPort_PgoutputInteropGoopgToPG` survival

Dropping the `t.Skip` on `TestPort_PgoutputInteropGoopgToPG` after this
slice exposed a different upstream probe — `fetch_table_list` (the
relation-list probe shipped earlier than the column-list probe):

```sql
SELECT DISTINCT n.nspname, c.relname, gpt.attrs
  FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN ( SELECT (pg_get_publication_tables(VARIADIC
                   array_agg(pubname::text))).*
           FROM pg_publication
           WHERE pubname IN ( … )) AS gpt
      ON gpt.relid = c.oid
```

This shape uses the IndirectionStar `(srf(...)).*` form **inside a
derived subquery whose argument list contains an aggregate**. The
non-aggregate variant was closed in loop 4 — the parse-time rewrite
moves the SRF into a FROM-clause `TableFuncRef` and the analyzer's
`tableFuncColumns` hands the outer scope the SRF's static three-column
shape. The aggregate-arg variant skips the rewrite (parser passes nil
`onAggregate`) and the planner lowers it via `ProjectSet` (loop 5).
The analyzer's `synthesizeSubqueryTable` does not yet expand
`*parser.IndirectionStar` targets, so it falls back to `?column?1`
and outer references like `gpt.attrs` raise

```
42703: column "attrs" does not exist
```

Pinned (failing-as-Skip) by `TestPlanFetchTableListAggDerivedSubquery`
in the planner package; fix-action: extend
`synthesizeSubqueryTable`'s target-list walk to recognise
`*parser.IndirectionStar` whose source `*parser.FuncCall` has a known
composite return shape (currently only `pg_get_publication_tables`)
and emit the matching three columns. Tracked as the next M0103-0008
sub-step.

## Verification

```
go test -race -count=1 -timeout 300s \
  ./internal/parser/ ./internal/planner/ ./internal/analyzer/ \
  ./internal/executor/ ./internal/server/ ./internal/wal/ \
  ./internal/catalog/
```

— all green. Planner regression tests
`TestPlanLateralSrfArgResolvesAgainstLeftFromItem` and
`TestPlanFetchTableListAggDerivedSubquery (Skip)` pass; executor
regression tests `TestLateralPgGetPublicationTablesFromOuterRef` and
`TestLateralPgGetPublicationTablesUnknownYieldsZero` pass.
