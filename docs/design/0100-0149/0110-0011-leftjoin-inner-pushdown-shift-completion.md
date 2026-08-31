# 0110-0011 — LEFT JOIN inner-only pushdown shift/classify completion

**Status:** accepted
**Milestone:** M0110-0003 (pg_amcheck TAP port) — residual #2 root cause
**Date:** 2026-06-15

## Summary

Generalizes the `0097-0023` fix. The LEFT JOIN inner-only ON-conjunct
pushdown relies on two sibling helpers staying in lockstep:

- `classifyConjunctSide` → `walkColumnRefs` (`internal/planner/pushdown.go`)
  decides whether a conjunct references only the inner (right) side and may
  therefore be pushed to a `Filter` on the inner plan.
- `shiftColumnRefsBy` (`internal/planner/planner.go`) rebases that conjunct's
  `ColumnRef.Index` values from outer-cumulative to inner-relative
  (`-leftWidth`) once it is pushed.

Both functions hand-enumerate `Expr` node kinds in a `switch`. `0097-0023`
fixed one missing kind (`*InExpr`) in `shiftColumnRefsBy`. Several more
sub-expression-bearing node kinds were still missing from **one or both**
functions, leaving the same latent `index out of range` panic class for any
pushed-down conjunct that routed a `ColumnRef` through one of them.

The concrete trigger this loop is **pg_amcheck's exclude-pattern anti-join**
(`--exclude-relation` / `--exclude-schema` / `--exclude-table`), which
panicked the backend — M0110-0003 residual #2.

A textbook instance of the recurring *sibling code paths must stay in sync*
failure class (auto-memory `pattern_sibling_paths_must_agree`).

## The bug, concretely

`pg_amcheck` (`src/bin/pg_amcheck/pg_amcheck.c`) builds, for each exclusion
pattern, an anti-join CTE shaped like the `toast` CTE:

```sql
, toast (oid, nspname, relname, relpages) AS (
  SELECT t.oid, 'pg_toast', t.relname, t.relpages
  FROM pg_catalog.pg_class t
  INNER JOIN relation r ON r.reltoastrelid = t.oid
  LEFT OUTER JOIN exclude_pat ep
    ON ('pg_toast' ~ ep.nsp_regex OR ep.nsp_regex IS NULL)
   AND (t.relname  ~ ep.rel_regex OR ep.rel_regex IS NULL)
   AND ep.heap_only
  WHERE ep.pattern_id IS NULL
    AND t.relpersistence != 't')
```

`exclude_pat` is a **5-column** VALUES build relation
`(pattern_id, nsp_regex, rel_regex, heap_only, btree_only)`. The LEFT JOIN's
left side is `(pg_class t JOIN relation r)`, width ≈ 42.

1. The conjunct `('pg_toast' ~ ep.nsp_regex OR ep.nsp_regex IS NULL)`
   references only `ep` (the inner side). `walkColumnRefs` recurses into the
   `~` `BinaryOp` and sees `ep.nsp_regex` at absolute index `leftWidth+1 = 43`
   → `classifyConjunctSide` returns `sideRight` → the conjunct is pushed to a
   `Filter` on the inner (`exclude_pat`) plan and rebased by `-leftWidth`.
2. **Bug:** `shiftColumnRefsBy` shifts the `~`-operand `ColumnRef` (its
   `BinaryOp` case is handled), but `ep.nsp_regex IS NULL` is an `*IsNullExpr`,
   which fell through to the `default` arm and was returned unchanged. Its
   inner `ColumnRef` kept absolute index **43**.
3. Execution: the inner `Filter` evaluates the predicate against an
   `exclude_pat` row of width **5**. `MaterializedSlot.Get(43)` → panic
   `index out of range [43] with length 5`, surfaced via
   `joinOp.Open → drainRowsCtx → filterOp.Next → evalExprSlot`.

The 4-way `index` exclusion CTE (same anti-join shape, but with the relation
on the *outer* side of the LEFT JOIN) did **not** crash, which is why earlier
analysis pinned the fault to the `toast` sub-CTE.

## Fix

Make the two sibling switches enumerate the same complete set of
sub-expression-bearing node kinds.

`shiftColumnRefsBy` (`planner.go`) — add the missing rebase cases:
`*IsNullExpr`, `*IsBoolExpr`, `*IsDistinctFromExpr`, `*CollateExpr`,
`*RowExpr`. (`*CastExpr`, `*BinaryOp`, `*UnaryOp`, `*FuncCall`, `*CaseExpr`,
`*ExtractExpr`, `*InExpr` were already handled.)

`walkColumnRefs` (`pushdown.go`) — add the matching descend cases so
classification *sees* every inner/outer ref: `*CastExpr`, `*IsNullExpr`,
`*IsBoolExpr`, `*IsDistinctFromExpr`, `*CollateExpr`, `*RowExpr`.

The `*CastExpr` addition to `walkColumnRefs` is the dangerous-direction twin:
before it, `cast(outer.col) = inner.col` had its outer `ColumnRef` hidden
inside the cast, so the classifier saw only the inner ref and *wrongly* tagged
the whole (mixed) conjunct `sideRight` — pushing an outer-referencing conjunct
below the LEFT JOIN. Now the classifier sees both refs → `sideMixed` → the
conjunct correctly stays in the join `Predicate`.

Subquery / correlated forms (`*SubqueryExpr`, `*ExistsExpr`,
`*ArraySubqueryExpr`, `*OuterColumnRef`, `*InExpr` with `Plan != nil`) remain
out-of-scope for pushdown (`walkColumnRefs` reports them via `onOuter`) and
are never shifted — their plans live in a separate column scope.

## Tests

- `internal/planner/shift_colrefs_in_test.go`
  - `TestShiftColumnRefsByIsNullExpr` — reproduces the pg_amcheck conjunct
    `('pg_toast' ~ ep.nsp_regex OR ep.nsp_regex IS NULL)` at `leftWidth=42`:
    asserts `classifyConjunctSide == sideRight` (both refs seen) and that the
    `IS NULL` operand shifts to inner ordinal `1` (was unshifted at `43`).
  - Existing `TestShiftColumnRefsByInExpr{,Nested}` still pass.
- End-to-end (live capped goopg server, this loop):
  - The exact 5-col VALUES anti-join CTE over `pg_class` returns rows with no
    panic.
  - The upstream `pg_amcheck --exclude-relation 'pg_catalog.*'` and
    `--exclude-schema pg_catalog` both exit `0` with **no** `index out of
    range` / panic in the server log (previously: backend panic).

Gates: `go build ./...`, `go vet ./internal/planner/`, `go test
./internal/planner/`, `go test ./internal/executor/` all clean. TPC-H Q12/Q13
spot-check: the bench data dir is currently an unloaded husk (no `tpch` role),
so `scripts/tpch-spotcheck.sh` SKIPped; the change only *adds* previously
absent recursion to two pushdown helpers (strictly more-correct classification
and shifting) and leaves every already-correct plan untouched — TPC-H Q12
(no LEFT JOIN inner-only `IS NULL` pushdown) and Q13 (LEFT JOIN with a
`NOT LIKE` `UnaryOp`, no new node kind) do not route through any newly handled
node.

## Follow-ups (now unblocked)

- Port the `--exclude-schema` / `--exclude-table` sections of
  `002_nonesuch.pl` (M0110-0003 residual #2) now that the anti-join no longer
  panics.
- The other residual of `002_nonesuch.pl` — the `datconnlimit = -2`
  invalid-database filter — remains blocked on a runtime `pg_database`
  shared-catalog write (auto-memory
  `goopg_no_runtime_shared_catalog_inplace_update`), a separate capability.
