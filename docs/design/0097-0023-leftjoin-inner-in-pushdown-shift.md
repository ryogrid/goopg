# 0097-0023 — LEFT JOIN inner-only `IN` pushdown index-shift fix

**Status:** accepted
**Milestone:** M0097-0023 (Port DDL / index / cluster / vacuum regress tests)
**Date:** 2026-06-06

## Summary

Fixes the latent crash that blocked populating `pg_constraint`: psql `\d` /
`\d+` panicked `index out of range [25] with length 25` in `Slot.Get`
whenever `pg_constraint` returned a non-empty result. The panic was a
planner rewriter gap, **not** a catalog/virtual-table problem.

`shiftColumnRefsBy` (`internal/planner/planner.go`) — the helper that rebases
a LEFT JOIN's inner-only ON conjunct when it is pushed to a Filter on the
inner plan — had no `*InExpr` case. Its sibling classifier
`walkColumnRefs`/`classifyConjunctSide` (`internal/planner/pushdown.go`)
**does** recurse into the literal-list form of `IN`. The two paths
disagreed: the classifier tagged `con.contype IN ('p','u','x')` as
inner-only and the optimizer pushed it down, but the rewriter left the
`contype` `ColumnRef` at its outer-cumulative index. At execution the inner
Filter row is only as wide as the inner relation, so the unshifted index
addressed one past the end.

A textbook instance of the recurring *sibling code paths must stay in sync*
failure class — see `pattern_sibling_paths_must_agree` in auto-memory.

## The bug, concretely

psql `\d` issues (abbreviated):

```sql
... FROM pg_index i
    LEFT JOIN pg_constraint con
      ON (con.conrelid = i.indrelid
          AND con.conindid = i.indexrelid
          AND con.contype IN ('p','u','x'))
...
```

Resolution + planning (`planFromItem`, `planner.go`):

1. `rightBinding.offset = len(leftCtx.schema)` = pg_index width **22**.
2. `con.contype` resolves to absolute index `offset + ordinal = 22 + 3 = 25`.
3. For `JoinTypeLeft`, `classifyConjunctSide` walks each ON conjunct.
   `walkColumnRefs` recurses into the literal-list `InExpr`, sees only
   right-side indices, and returns `sideRight`. The conjunct is moved to a
   `Filter` wrapping the **inner** (pg_constraint) scan, and each conjunct is
   rebased by `shiftColumnRefsBy(c, -leftWidth)` so its indices point into the
   inner-only row.
4. **Bug:** `shiftColumnRefsBy`'s `switch` handled `BinaryOp`, `CastExpr`,
   `UnaryOp`, `FuncCall`, `CaseExpr`, `ExtractExpr` — but `*InExpr` fell to the
   `default` arm, which returns the node unchanged. So `contype` stayed at
   index **25**.
5. Execution: the inner Filter evaluates `contype IN (...)` against a
   pg_constraint row of width **25** (indices 0–24). `evalInExpr`
   (`internal/executor/expr.go`) calls `Slot.Get(25)`
   (`internal/executor/opnode.go:98`) → `s.Cells[25]` → panic
   `index out of range [25] with length 25`.

This was masked only because `pg_constraint`'s virtual rows were empty: a
zero-row inner Filter never evaluates the predicate, so `Get` was never
called with the bad index. The moment `pg_constraint` produced any row
(the prerequisite for COMMENT / `pg_description` description-join queries and
for assigning real constraint OIDs), `\d` crashed.

## Fix

Add an `*InExpr` case to `shiftColumnRefsBy` that mirrors `walkColumnRefs`:
shift the `Operand` and every `List` element by `delta`, preserve
`Negated` / `NotEqualAny` / `IsNonCorrelated`, and leave `Plan` untouched.
The subquery form (`Plan != nil`) is never pushed down — `walkColumnRefs`
reports it out-of-scope via `onOuter` — and its `Plan` lives in a separate
column scope that must not be shifted, so passing it through verbatim is
correct and defensive.

```go
case *InExpr:
    list := make([]Expr, len(x.List))
    for i, item := range x.List {
        list[i] = shiftColumnRefsBy(item, delta)
    }
    return &InExpr{
        pos:             x.Pos(),
        Operand:         shiftColumnRefsBy(x.Operand, delta),
        Negated:         x.Negated,
        NotEqualAny:     x.NotEqualAny,
        Plan:            x.Plan,
        List:            list,
        IsNonCorrelated: x.IsNonCorrelated,
    }
```

## Tests

- `internal/planner/shift_colrefs_in_test.go`
  - `TestShiftColumnRefsByInExpr` — a bare `IN` whose operand sits at
    `leftWidth+ordinal` shifts to its inner-relative ordinal; the original
    tree is not mutated.
  - `TestShiftColumnRefsByInExprNested` — `IN` nested under a `BinaryOp` is
    reached and shifted.
- `internal/executor/leftjoin_inner_in_pushdown_test.go`
  - `TestLeftJoinInnerOnlyInPushdown` — end-to-end `LEFT JOIN` over plain
    user tables with an inner-only `kind IN (...)` conjunct. Reproduces the
    identical pushdown+shift path without catalog-stub confounds. Verified
    to panic `index out of range [2] with length 2` with the fix reverted,
    and to return the correct `(1,'p'),(2,NULL),(3,'x')` LEFT JOIN result
    with the fix in place.

Planner suite green; executor suite shows only the two pre-existing,
unrelated failures (`TestPgGetPublicationTablesRelidMatchesPgClassOid`,
`TestToastByteaRoundTrip`) that fail identically on clean HEAD.

## Follow-ups (now unblocked)

This removes the join-layer blocker recorded in
`0097-0023-named-check-constraint-violations.md` and in auto-memory
(`pg_constraint_population_latent_join_crash`). A subsequent loop can:

1. Assign real OIDs to named CHECK constraints and populate `pg_constraint`
   rows, then re-verify `\d` / `\d+` over tables with constraints.
2. Wire COMMENT ON storage into `pg_description` and verify the
   description-join queries.

Both should add their own coverage; this doc only closes the index-shift
crash.
