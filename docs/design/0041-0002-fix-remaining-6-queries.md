# 0041-0002 — Fix Remaining Divergent TPC‑H Queries

**Status:** in‑progress (Q3, Q11 closed 2026‑05‑04; Q5, Q7, Q9, Q10
still open)
**Parent milestone:** M0041
**Date:** 2026‑05‑03 (initial), 2026‑05‑04 (revision)

## 1. Objective

Close the remaining DIVERGENT TPC‑H parity queries that still return
incorrect results. Target: identical ≥ 19, divergent = 3 (Q1 + Q8 +
Q14 numeric precision only, allowlisted in `parity_test.go`).

## 2. Current State (2026‑05‑04 after bindings‑posMap landing)

```
identical=15 divergent=7 errored=0
```

Q3 and Q11 are now IDENTICAL thanks to the bindings‑posMap +
`remapAggExprsWithBindings` split (HAVING predicate is no longer
inadvertently remapped). Q1 / Q8 / Q14 are precision‑only divergences
on the `knownDivergences` allowlist.

| Query | Tables | MHJ? | Status (2026‑05‑04) | Root Cause |
|-------|--------|------|---------------------|-----------|
| Q5 | 6 | No (star‑guard: supplier degree=3) | DIVERGENT (0 vs 1) | Binary‑tree ColumnRef alignment beyond Filter/Project/Sort |
| Q7 | 6 | Yes (inside inline‑view subquery; `nation n1, n2` self‑join) | DIVERGENT (3 vs 1) | Outer query ColumnRefs into subquery output |
| Q9 | 6 | No (star‑guard: lineitem centre, multi‑col partsupp join) | DIVERGENT (0 vs 6) | Binary‑tree ColumnRefs + multi‑col join |
| Q10 | 4 | No | DIVERGENT (0 vs 1) | Smallest case; investigate first |
| Q3 | 3 | Yes | IDENTICAL | Closed by `remapAggExprsWithBindings` |
| Q11 | 3 | Yes (subquery) | IDENTICAL | Closed by `walkSubqueryPlans` recursion + agg‑split |

## 3. Fix A: Subquery Plan Traversal

`remapPosMapAfterRewrite` currently walks `Filter`, `Project`, `Sort`,
`Aggregate`, `Join` nodes.  It does **not** descend into `SubqueryExpr.Plan`
or `InExpr.Plan` inner plans.  For Q7 (inline‑view subquery) and Q10/Q11
(correlated subqueries), the inner plan contains its own join tree or MHJ
whose ColumnRefs are not remapped.

**Fix:** In `remapPosMapAfterRewrite`, after processing the current node,
walk its expression tree and recursively call `remapPosMapAfterRewrite` on
any embedded `SubqueryExpr.Plan` or `InExpr.Plan`.

```go
// After the switch, walk expressions to find subquery plans
walkExprsForSubqueries(node, func(inner Plan) {
    remapPosMapAfterRewrite(inner, nil)
})
```

This ensures the MHJ inside Q7's inline‑view subquery gets its schema
sorted by OID, and the binary join tree inside Q10/Q11's subqueries gets
the posMap remap.

## 4. Fix B: Binary Join ColumnRef Alignment (Q5/Q8/Q9)

These queries use the bushy DP's binary join trees (MHJ is rejected by the
star‑graph guard).  The `binaryTreePosMapOf` function already collects
SeqScan leaves and builds a posMap, but the posMap may be an identity
(left‑deep trees) or may not reach the correct join subtree.

**Fix:** Verify that `binaryTreePosMapOf` correctly collects all table
scans for Q5/Q8/Q9.  If the bushy DP produces a non‑left‑deep tree where
DFS order ≠ FROM order, the posMap should remap correctly.  If the DP
produces a left‑deep tree, the ColumnRefs are already correct (no remap
needed) and the 0‑row issue must be elsewhere (investigate execution).

## 5. Verification

| Test | Expected |
|------|----------|
| `TestTPCHResultParity` | identical ≥ 19, divergent = 3 (Q1+Q8+Q14 precision), errored = 0 |
| `TestRunTPCHQueriesAgainstSyntheticData` | 22/22 PASS |
| `go test ./...` | no new failures |

## 6. Landed (2026‑05‑04)

- `internal/planner/bushy.go`:
  - new `scanKey {table, alias}` to disambiguate self‑joins;
  - `buildBindingsPosMap` builds an offset map keyed by
    `(table*, alias)` and remaps FROM‑clause offsets to actual scan
    offsets;
  - `applyJoinTreePosMap` walks Filter / Project / Sort below any
    Aggregate without touching Join keys (which are already in
    per‑subset coordinates) or MHJ outputs (already OID‑sorted);
  - `remapAggExprsWithBindings` remaps **only** GroupExprs / Agg.Arg
    on the Aggregate at/below the wrapping HAVING Filter, leaving
    the HAVING predicate (which uses agg‑output indices) untouched;
  - `walkSubqueryPlans` hoisted into a single recursive closure
    used from every node arm, with new coverage for `CaseExpr` and
    `ExtractExpr`;
  - `remapByPosMap` extended to walk `InExpr.Operand` and the arms
    of `CaseExpr`.
- `internal/planner/plan.go`: `SeqScan.Alias`.
- `internal/planner/planner.go`: forward `rv.Alias` to `SeqScan`,
  switch the post‑aggregate hook from `remapWithBindings +
  remapExprRefsToMHJ` to `remapAggExprsWithBindings`.

Result: parity 14→15 identical, Q3 and Q11 closed, no regressions.

## 7. Still Open

The bindings‑posMap covers the common cases but Q5, Q7, Q9, Q10
remain divergent. Likely follow‑ups:

- Audit the bushy DP plan shapes for Q5/Q9 — look for nodes the
  walker skips (e.g. nested Filter‑below‑Filter, Aggregate‑below‑
  Aggregate, or unnested join trees outside the recognised arms).
- Q7 inline‑view subquery: outer `Project.Targets` may reference
  the subquery's pre‑remap output columns; the recursive subquery
  walk runs `posMap=nil` so the inner plan only gets its own
  MHJ‑posMap pass — the outer query's bindings against the
  subquery's RangeVar output may need a separate fix.
- Q10's 4‑table query is the smallest; reproduce its plan and
  diff goopg's output against upstream column by column to localise
  the misalignment.
