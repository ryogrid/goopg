# Design 0061-0001 — EXISTS / NOT EXISTS Unnesting to Semi-/Anti-Join

**Milestone:** M0061-0001
**Status:** approved (autonomous)
**Supersedes:** the deferred M0058-0002

## Context

`EXISTS(subq)` and `NOT EXISTS(subq)` predicates currently flow
through `planExistsExpr()` (`internal/planner/planner.go:2790-2798`),
which wraps the inner plan in an `ExistsExpr` AST node. The
executor's `evalExistsExpr()`
(`internal/executor/expr.go:742-801`) opens the inner plan once per
outer row, calls `Next()` once, and returns a boolean. M0058-0001
added a non-correlated cache, but **correlated** EXISTS still
re-runs per outer row.

Q22 (`NOT EXISTS (SELECT * FROM orders WHERE o_custkey = c_custkey)`)
times out at 300 s on SF=1 (verified 2026-05-07) for exactly this
reason. Q4 / Q21 share the shape.

## Plan

Convert `EXISTS(subq)` / `NOT EXISTS(subq)` to a hash semi-join /
anti-join when the correlation predicate is an equijoin between an
outer column and an inner base-table column. Reuse the M0040
IN-unnesting infrastructure (`unnest.go`) verbatim — the difference
is only the output schema and per-probe-row emission semantics.

### Planner changes

1. **`JoinTypeSemi` / `JoinTypeAnti`** added to the `JoinType` enum
   in `internal/planner/plan.go`. Both keep `Algo == JoinAlgoHash`
   in the only path we generate.
2. **`Join.schema`** for semi/anti = `Left.Output()` (the outer side).
   Inner side is consumed only for matching, never projected.
3. **`unnestExistsExpr()`** in `internal/planner/unnest.go` mirrors
   `unnestInExpr()`:
   - Walks the Filter's predicate looking for an `ExistsExpr`.
   - Calls the existing `collectUnnestParams()` to confirm every
     `OuterColumnRef` is in an equijoin pair.
   - Clones the inner plan with `OuterColumnRef → ColumnRef`
     replacement.
   - Produces a `Join{Type: JoinTypeSemi or JoinTypeAnti,
     Algo: JoinAlgoHash, Left: outer, Right: inner_clone, ...}`
     and removes the original `EXISTS` conjunct from the Filter.
4. **Hookup**: `unnestSubqueriesInPlan()` gets a third pull-up loop
   identical in shape to the existing IN loop, calling
   `findExistsExprInExpr` / `unnestExistsExpr`.

### Executor changes (`internal/executor/operators_join_agg.go`)

1. **`openLazyHashJoin`** — when `plan.Type` is Semi or Anti, force
   the right (inner) side as the build side regardless of
   `BuildLeft` (semi/anti's outer-row-preservation semantics depend
   on the *left* being the probe stream).
2. **`nextLazy`** — extended to emit:
   - **Semi** — for each probe row, return the probe row exactly
     once if `lazyHash[key]` is non-empty, otherwise skip. Output
     row width = `lazyLW`.
   - **Anti** — for each probe row, return the probe row exactly
     once if `lazyHash[key]` is empty (or key is NULL), otherwise
     skip.
3. **Output schema** — the operator's `o.schema` is taken from
   `plan.Output()`, which for Semi/Anti is the outer (left) schema
   only. No fallback concatenation.

### NULL semantics

- Semi join: NULL key on the probe side never matches (consistent
  with hash-join NULL handling already in
  `evalHashKey`).
- Anti join (`NOT EXISTS`): a NULL inner-key row cannot match by
  equality, so the outer row is **kept**. This matches PostgreSQL's
  `NOT EXISTS` (which is true unless at least one matching row
  exists). This differs from `NOT IN`, which has tri-valued logic
  and would suppress the outer row on a NULL inner. M0061-0001
  scope is `NOT EXISTS` only — `NOT IN` unnesting stays out of
  scope.

### Inner-side dedup

Not needed for semi/anti: the per-probe-row "match found?" check
already collapses N inner rows per key into one outer-row emission.

## Acceptance

- Q22 (`NOT EXISTS`) completes in < 60 s on SF=1.
- Q4, Q21 (`EXISTS` / `NOT EXISTS` on lineitem) each < 60 s.
- EXPLAIN of Q22 / Q4 / Q21 shows `Hash Join` with
  `Type=JoinTypeSemi` or `JoinTypeAnti`, no surviving SubPlan that
  re-runs per outer row.
- New tests in `internal/planner/exists_unnest_test.go` cover:
  EXISTS positive case, NOT EXISTS positive case, non-equijoin
  rejection (falls back to SubPlan), and Q22 end-to-end.
- Existing M0040 IN tests (`unnest_test.go`,
  `non_correlated_subquery_test.go`) all still PASS.
- `go test ./...` PASS.

## Risks / non-goals

- **NOT IN.** Out of scope — needs tri-valued logic.
- **EXISTS with non-equijoin correlation** (range / `LIKE`).
  Falls through to existing SubPlan; `canUnnestExistsExpr` returns
  false.
- **Subqueries in SELECT list / GROUP BY.** Out of scope — only
  Filter-level EXISTS is unnested.
- **EXISTS inside a disjunction.** A `WHERE p OR EXISTS(...)`
  cannot be unnested as a join — `findExistsExprInExpr` requires
  the EXISTS to be a top-level conjunct of the Filter predicate
  (same as IN-unnesting).
