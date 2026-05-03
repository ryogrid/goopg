# Milestone 0041 — Close Remaining TPC‑H Result-Parity Gaps

**Status:** in‑progress (parity 14→15 identical as of 2026‑05‑04;
Q5/Q7/Q9/Q10 still divergent, target ≥19 not yet met)
**Depends on:** M0039 (planner column-index alignment), M0038 (multi-way hash join), M0034 (bushy DP)
**Drives:** Eliminate the remaining 8 DIVERGENT TPC‑H parity queries (currently 14 identical, 8 divergent, 0 errored). Target: all 22 queries IDENTICAL (precision‑only differences for Q1/Q14 accepted as known limit).

## Context

M0039 improved TPC‑H parity from identical=13 to identical=14 by sorting
MultiHashJoin tables by OID (FROM‑clause creation order). This fixed the
column‑order mismatch between binary‑join‑tree output (FROM order) and
MHJ output (DFS tree‑walk order) for queries using MultiHashJoin.

The remaining 8 divergent queries fall into two categories:

| Category | Queries | Root cause |
|----------|---------|-----------|
| **Non‑MHJ queries** (2 tables, or chain detection rejected) | Q5, Q8, Q10 (binary hash‑join only, no MHJ) | ColumnRef indices from bushy DP / pushdown still misaligned for some join shapes |
| **MHJ queries with residual misalignment** | Q3, Q7, Q9, Q11 | OID sort fixed schema order, but subquery plans or complex aggregate expressions may still reference stale positions |
| **Numeric precision** | Q1, Q14 | goopg v0 numeric formatting is truncated; accepted as known limitation |

## Required Design Docs

1. `docs/design/0041-0001-close-parity-gaps.md` — Analysis of the remaining 8
   divergent queries, root‑cause tracing per query, proposed fixes for each
   category, verification strategy.

## Definition of Done

1. **Non‑MHJ binary‑join fix** (`bushy.go` / `pushdown.go`):
   - For 2‑table queries (Q6, Q12, etc.) where pushdown handles the join,
     verify ColumnRef indices are correct.
   - For 3‑table queries where bushy DP runs but chain detection rejects
     (star graph, scope boundary), verify binary‑join ColumnRef indices.

2. **Filter ColumnRef remapping in `planSelect`**:
   - The `buildAggregateStage` resolves ColumnRefs against `ctx.bindings`
     which use pre‑rewrite order.  After `rewriteMultiWayChain`, ensure
     the filter predicates referencing the MHJ output are correctly aligned.

3. **TPC‑H parity**: 0 queries goopg‑errored, ≤ 2 divergent (Q1 + Q14
   numeric precision only), ≥ 20 identical.

4. **`TestRunTPCHQueriesAgainstSyntheticData`**: 22/22 PASS (already met).

5. **Config**: `shared_buffers=2048MB` (2 GiB), `GOMEMLIMIT=20GiB`.

## Reference

- `internal/planner/bushy.go` — `rewriteMultiWayChain`, `collectMultiHashTables`,
  `buildJoinFromDP`, `remapKeyToSubset`
- `internal/planner/planner.go` — `planSelect`, `pushPredicatesIntoCrossJoins`,
  `splitEqualityForHash`
- `internal/planner/pushdown.go` — `pushOneConjunct`
- `internal/executor/expr.go` — `compareDatum`, `promoteCrossKind`, arithmetic
  string‑to‑numeric conversion
- `analysis/tpch-q20-bottleneck-analysis.md` — Q20 complexity analysis
