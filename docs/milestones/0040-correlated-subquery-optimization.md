# Milestone 0040 — Correlated Subquery Optimization

**Status:** planned
**Depends on:** M0038 (multi-way hash join), M0033 (subquery unnesting)
**Drives:** Reduce TPC-H Q20 execution time from >1 h to ≤120 s by eliminating per-outer-row subquery re-execution. Enable unnesting of `IN (subquery)` expressions.

## Context

TPC-H Q20 contains three correlated subqueries (two `IN`, one scalar aggregate)
nested across two levels:

```
s_suppkey IN (parts = ps_partkey IN (part) AND ps_availqty > SCALAR(lineitem))
```

goopg v0 executes **every** correlated subquery by re-opening the inner plan
from scratch for **each** outer row (`executor/expr.go:641‑677`). For Q20's
SF=1 dataset (800K partsupp rows × 4.4M lineitem rows), this produces
~3.5 × 10¹² tuple probes — exceeding any practical time budget.

The subquery unnest pass (M0033) only handles `SubqueryExpr` (scalar
aggregates with GROUP BY) and does not look for `InExpr` (`column IN
(subquery)`).  `InExpr` subqueries are executed entirely at the executor
level with no planner rewrite.

## Required Design Docs

1. `docs/design/0040-0001-subquery-caching-and-unnest.md` — Two‑part design:
   - **Cache**: per‑outer‑key caching of correlated subquery results in
     `collectInValues` / `evalSubquery`
   - **Unnest**: extend the unnest pass to recognise `InExpr` and rewrite
     it as a hash semi‑join

## Definition of Done

### M0040‑0001: Materialise subquery results per outer‑key

1. **Subquery cache**: Add a `subqueryCache` field to the executor's
   `evaluateContext` (or a per‑query cache accessible from
   `collectInValues` / `evalSubquery`).

2. **`collectInValues` caching** (`executor/expr.go:637`):
   - Before `Build(x.Plan)` / `Open()`, build a cache key from the
     outer‑row value(s) — `datumKey(outerRefValue)`.
   - If the cache has an entry, return it immediately.
   - Otherwise, execute the inner plan, store the result, and return.

3. **`evalSubquery` caching** (`executor/expr.go:720`):
   - Same pattern — cache scalar subquery results by outer‑key.

4. **Cache invalidation**: When `ctx.OuterRows` stack depth changes
   (subquery scope changes), the cache entries from the previous level
   are invalidated.

5. **Verification**:
   - Q20's lineitem scalar subquery is evaluated once per distinct
     `(l_partkey, l_suppkey)` pair, not per partsupp row.
   - Q20 total execution time ≤ 120 s at SF=1 partial data.

### M0040‑0002: Unnest `IN (subquery)` → semi‑join

1. **Planner detection** (`internal/planner/unnest.go`):
   - Extend `findSubqueryInExpr` to also visit `*planner.InExpr`.
   - Add `canUnnestInExpr`: check that the inner plan is a simple
     `SELECT col FROM table WHERE col = outer_ref` (no aggregate,
     single table).

2. **Semi‑join rewrite**:
   - Rewrite `column IN (SELECT inner_col FROM … WHERE inner_col = outer.col)`
     into a semi hash‑join: `JoinTypeSemi(outer_plan, inner_plan)`
     with `drainRows` dedup on the inner side.
   - Use the existing `clonePlanReplacingOuter` to replace `OuterColumnRef`
     with `ColumnRef` in the inner plan.

3. **Verification**:
   - Q20's outermost `s_suppkey IN (subquery)` is rewritten as a single
     hash semi‑join between `(supplier⋈nation)` and `partsupp`.
   - `TestBushyPlanWithUnnest` style tests pass.

### M0040‑0003: End‑to‑end verification

1. **TPC‑H synthetic data**: `TestRunTPCHQueriesAgainstSyntheticData` —
   22/22 PASS; no regression from M0039 baseline.

2. **TPC‑H parity**: `TestTPCHResultParity` — no regression (identical ≥ 13,
   errored = 0).

3. **HammerDB SF=1 power test**: Q20 completes within ≤ 120 s.
   Config: `shared_buffers=2048MB` (2 GiB), `GOMEMLIMIT=20GiB`.

## Reference

- `internal/executor/expr.go` — `collectInValues` (line 637), `evalSubquery`
  (line 720), `evalInExpr` (line 601)
- `internal/planner/unnest.go` — `findSubqueryInExpr` (line 45),
  `canUnnestSubquery` (line 91), `clonePlanReplacingOuter` (line 307)
- `internal/planner/plan.go` — `InExpr` (line 106), `SubqueryExpr` (line 134)
- `internal/executor/operators_join_agg.go` — hash join operator, semi‑join
  support
- `analysis/tpch-q20-bottleneck-analysis.md` — detailed complexity analysis
