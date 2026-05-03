# 0041-0002 — Fix Remaining 6 Divergent TPC‑H Queries

**Status:** draft
**Parent milestone:** M0041
**Date:** 2026-05-03

## 1. Objective

Close the remaining 6 DIVERGENT TPC‑H parity queries (Q5, Q7, Q8, Q9, Q10,
Q11) that still return 0 rows. Target: identical ≥ 18, divergent ≤ 4 (Q1 +
Q14 precision only).

## 2. Current State

```
identical=14 divergent=8 errored=0
```

| Query | Tables | MHJ? | Root Cause |
|-------|--------|------|-----------|
| Q5 | 6 | No (star‑guard: supplier degree=3) | Binary join ColumnRefs |
| Q7 | 6 | Yes (inside inline‑view subquery) | Subquery plan not remapped |
| Q8 | 8 | No (complex multi‑table) | Binary join ColumnRefs |
| Q9 | 6 | No (star‑guard: lineitem centre) | Binary join ColumnRefs |
| Q10 | 4 | No (subqueries) | Subquery plan not remapped |
| Q11 | 3 | Yes (subquery) | Subquery plan not remapped |

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
| `TestTPCHResultParity` | identical ≥ 18, divergent ≤ 4 (Q1+Q14 precision), errored = 0 |
| `TestRunTPCHQueriesAgainstSyntheticData` | 22/22 PASS |
| `go test ./...` | no new failures |
