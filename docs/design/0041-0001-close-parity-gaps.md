# 0041-0001 — Close Remaining TPC-H Parity Gaps

**Status:** draft
**Parent milestone:** M0041
**Date:** 2026-05-03

## 1. Objective

Eliminate the remaining 8 DIVERGENT queries in `TestTPCHResultParity` so all
22 TPC-H queries return identical results to upstream PostgreSQL (precision
differences for Q1/Q14 accepted as a known v0 limitation).

## 2. Current State (after M0039 MHJ OID sort)

```
identical=14 divergent=8 errored=0
```

| Query | Status | Category |
|-------|--------|----------|
| Q1 | DIVERGENT (precision) | Numeric formatting |
| Q3 | DIVERGENT (0 rows) | MHJ + aggregate |
| Q5 | DIVERGENT (0 rows) | Non‑MHJ (6 tables, likely star‑shaped) |
| Q7 | DIVERGENT (0 rows) | MHJ + subquery |
| Q8 | DIVERGENT (0 rows) | Non‑MHJ (8 tables) |
| Q9 | DIVERGENT (0 rows) | Star‑shaped (Q9), chain‑rejected |
| Q10 | DIVERGENT (0 rows) | Non‑MHJ (4 tables) |
| Q11 | DIVERGENT (0 rows) | MHJ + aggregate |
| Q14 | DIVERGENT (precision) | Numeric formatting |

### 2.1 How the MHJ OID sort fixed one query

In the MHJ (used by queries where `collectMultiHashTables` found ≥3
sequential hash joins), the output schema was built with tables in **DFS
tree‑walk order**.  The binary join tree output used **FROM‑clause order**.
`rewriteMultiWayChain` now sorts collected scans by catalog OID (FROM order)
before building the MHJ schema.  This eliminates the ColumnRef‑index
mismatch for all MHJ‑using queries **where no subquery unnest or aggregate
rewire exists**.

### 2.2 What is still broken

#### Non‑MHJ queries (Q5, Q8, Q10)

These queries don't trigger MultiHashJoin chain detection.  Reasons:

- **Star‑shaped join graphs** (e.g. Q8 with 8 tables, Q5 with 6 tables):
  `collectMultiHashTables` rejects them (table‑degree > 2 guard).
- **2‑table joins** (Q10 has multiple 2‑table subqueries): bushy DP
  runs but produces binary joins only.
- **Scope boundaries** (Aggregate/Filter inside the join tree): the
  walk function stops at these, breaking the chain.

For non‑MHJ queries, the plan uses binary hash‑joins from
`buildJoinFromDP` (`remapKeyToSubset`).  While the join‑keys are
correctly remapped, **the scan order in the binary tree** may differ
from the **ColumnRef indices in parent expressions** (Filter, Project,
Aggregate) that were resolved against the **original CROSS tree's
FROM‑order** schema.

#### MHJ queries with subqueries (Q7)

Q7 has an inline‑view subquery whose inner plan produces its own MHJ.
The outer query references the subquery's Project output, not the MHJ
output directly.  The MHJ OID sort fixes the inner MHJ, but the **outer
query's ColumnRefs** may still reference wrong positions in the
subquery's output if the subquery's own ColumnRefs weren't remapped.

#### Numeric precision (Q1, Q14)

Goopg v0's `Datum.Format()` for numeric values truncates fractional
digits compared to PostgreSQL.  This is a know limitation from M0003
and is **not in scope for M0041**.

## 3. Fix Plan

### 3.1 Fix A: Remap Filter/parent ColumnRefs after `buildAggregateStage`

The `buildAggregateStage` in `planSelect` resolves ColumnRefs against
`ctx.bindings` which uses pre‑rewrite (binary‑tree) column order.
After `rewriteMultiWayChain` replaces the tree, the MHJ output ORDER has
changed (even after OID sort — the Tables field order differs from the
binary tree's DFS scan order).  The filter predicate and aggregate
expressions still use old positions.

**Fix:** After `rewriteMultiWayChain` returns, build a ColumnRef position
mapping from OLD (binary‑tree) to NEW (MHJ output) positions and remap
the Filter predicate of the wrapping node.  This is the `remapExprRefsToMHJ`
/ `remapByPosMap` approach from M0039 Fix B, but properly wired and tested.

### 3.2 Fix B: Apply the same remap after aggregate

After `buildAggregateStage` creates new Aggregate/Project/Sort nodes,
their ColumnRefs are resolved against the input context.  If the input
context uses the OLD binary‑tree positions, these ColumnRefs will be
wrong even after Fix A.  Run `remapExprRefsToMHJ` a second time.

### 3.3 Fix C: Binary‑join ColumnRef alignment (non‑MHJ queries)

For queries that don't use MultiHashJoin:
- The bushy DP creates binary hash‑join trees with ColumnRefs via
  `remapKeyToSubset` (fixed in M0039).
- The parent Filter/Project/Aggregate ColumnRefs are resolved against
  `ctx.bindings` which uses FROM order.
- The binary join tree's DFS scan order may differ from FROM order
  for bushy (non‑left‑deep) trees.

**Fix:** After `tryBushyDP` produces a bushy join tree, apply the same
OID‑sort logic to the **binary join tree's output schema** by walking
the SeqScan leaves and sorting their schemas.  Not trivial — requires
recursive schema rebuilding.

**Simpler alternative:** For non‑MHJ queries, fall back to
`pushPredicatesIntoCrossJoins` which produces a left‑deep tree in FROM
order (already aligned).  This trades optimal bushy join order for
correctness.  Acceptable for M0041 scope.

### 3.4 Implementation Order

1. **Fix A + B** (remap Filter after rewrite + after aggregate) — these
   are low‑risk refinements of the already‑existing M0039 remap
   infrastructure.  Expected to fix Q3, Q9, Q11 (MHJ queries).

2. **Fix C** (binary‑join alignment) — higher effort.  For now, just fall
   back to pushdown (left‑deep trees) for queries where bushy DP produces
   non‑left‑deel trees.  Expected to fix Q5, Q8, Q10.

3. **Q7 special case** (subquery) — ensure the remap traverses into
   subquery plans embedded in `SubqueryExpr` / `InExpr` nodes.

## 4. Verification

| Test | Expected |
|------|----------|
| `TestTPCHResultParity` | identical ≥ 20, divergent ≤ 2 (Q1 + Q14), errored = 0 |
| `TestRunTPCHQueriesAgainstSyntheticData` | 22/22 PASS |
| `go test ./...` | no new failures |

## 5. Reference

- `internal/planner/bushy.go` — `rewriteMultiWayChain`, `collectMultiHashTables`
- `internal/planner/planner.go` — `planSelect`, `buildAggregateStage`
- `internal/planner/pushdown.go` — `pushPredicatesIntoCrossJoins`
- `docs/milestones/0039-fix-planner-column-ref.md` — M0039 analysis
- `analysis/tpch-power-test-0039-final.md` — power test results

## 6. Outcome (2026‑05‑04)

The Fix A / Fix B / Fix C plan landed across three branch commits
(`a4ef440`, `b66698a`, `17474de`) and a final consolidation pass on
2026‑05‑04 that introduced the bindings‑posMap with `(table*, alias)`
self‑join disambiguation and split aggregate‑expression remap from
HAVING‑predicate remap.

Parity progression on the synthetic dataset:

| Stage | identical | divergent | errored |
|-------|----------:|----------:|--------:|
| Pre‑M0041 (M0039) | 14 | 8 | 0 |
| M0041 partial (Fix A/B/C posMap) | 15 | 7 | 0 |
| M0041 + executor & residual fixes | **17** | 5 | 0 |
| Target | ≥ 19 | 3 (Q1+Q8+Q14) | 0 |

Q3, Q5, Q10, Q11 are now IDENTICAL. Q7 (3 vs 1 rows) and Q9
(row=3 col=2 numeric mismatch) remain. See
`0041-0002-fix-remaining-6-queries.md` §7 for the open items.
