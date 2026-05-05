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

## 6. Landed (2026‑05‑04, second batch)

Q5 and Q10 are now IDENTICAL (parity 15→17). Five distinct fixes
across the executor and planner closed them:

- `internal/executor/multi_hash_join.go`:
  - **Chain `visited` tracking** — chains of 4+ tables (Q10's
    nation→customer→orders→lineitem) no longer loop back into a
    previously‑consumed build table on a later iteration.
  - **Branched chain build** — any already‑visited table can be the
    source of a new step (`srcTable`/`srcCol` pair on each step), so
    the probe can fan out to multiple subtrees in one MHJ. Q5's
    lineitem‑probe reaches both supplier→nation→region AND
    orders→customer through this.
  - **Per‑step `buildKeyCol`** — the build phase hashes each table
    on the column the chain actually probes (taken from the
    `MultiHashKey` whose chain step lands on that table), not the
    legacy "first key mentioning this table" heuristic. Fixes the
    Q5 supplier case where `s_nationkey` won the heuristic but the
    chain looked up by `s_suppkey`.

- `internal/planner/bushy.go`:
  - **Specific‑edge marking in DP**: `enumerateBushyPlans` now
    records `bestEdgeIdx` per DP step and marks only that specific
    `g.edges[i]` as used. The residual loop also matches by edge
    predicate identity. The two together stop the DP from silently
    consuming the second of two parallel equalities (TPC‑H Q9's
    partsupp↔lineitem `ps_suppkey=l_suppkey AND ps_partkey=
    l_partkey`).
  - **`reresolveJoinByName`**: Joins above an MHJ had keys in the
    pre‑rewrite subset‑FROM‑order schema. The new pass re‑binds
    `LeftKey` / `RightKey` / `Predicate` ColumnRefs by NAME against
    the post‑rewrite child schemas. `predRebind` disambiguates
    duplicate names (e.g. `INNER JOIN b ON a.id = b.id`) by
    classifying by the ORIGINAL `cr.Index < leftWidth`. `j.schema`
    is also refreshed so outer Joins see the current layout when
    they themselves rebind.

- `internal/planner/planner.go` + `remapTopProjection` in
  `bushy.go`: inline‑view subqueries' `Project` was added after the
  join‑tree rewrites and therefore kept FROM‑order indices. The new
  pass walks `Project` / `Sort` wrappers above the join tree
  (stopping at Filter / Aggregate / Join / MHJ) and remaps using
  the bindings posMap. Fixes the EXTRACT/arithmetic stale‑index
  errors in Q7/Q8/Q9.

- `internal/planner/plan.go`: `SeqScan.Alias` (already landed in
  the prior commit) carries the FROM alias; combined with
  `(table*, alias)` `scanKey` the MHJ correctly distinguishes
  `nation n1, nation n2` self‑joins.

## 7. Still open

- **Q7** (`nation n1, n2` self‑join inside inline‑view): goopg
  returns 3 rows vs upstream 1. The MHJ correctly distinguishes
  the two nation aliases via `scanKey`, but the outer aggregate
  picks up extra rows. Likely cause: `predRebind`'s `findUnique`
  treats a duplicate column name as ambiguous (both n2 and n1
  schemas have `n_name`), keeping the original index — but the
  original index might already point at the wrong alias for one of
  the two refs. Needs alias‑aware lookup in `findUnique` (use
  `(table*, alias)` keying analogous to `scanKey`).

- **Q9** at row=3 col=2 (5570 vs 5795). Row count now matches
  upstream (6/6); a single sum value disagrees. The residual
  `ps_partkey=l_partkey` is generated and kept in the Filter, but
  `pushOneConjunct` can't push it onto the inner Join because the
  conjunct's ColumnRef indices are global FROM‑order while the
  Join's schema is subset‑FROM‑order — `classifyConjunctSide` uses
  width comparisons that mis‑classify. Needs a name‑based side
  classification (similar to the `predRebind` approach) inside
  `pushOneConjunct`, or a coord‑translation pass on the residual
  conjunct before pushdown.

## 6.bis Older entry — Landed (2026‑05‑04, first batch)

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
