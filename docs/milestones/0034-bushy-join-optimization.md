# Milestone 0034 — DP-Based Bushy Join Optimization (DPccp-Style)

**Status:** accepted
**Depends on:** Milestone 0033 (subquery unnesting confirmed; CROSS join identified as remaining bottleneck), Milestone 0006 (planner statistics for cardinality estimates)
**Drives:** Eliminate the CROSS join explosion in Q2's outer 5-table comma-join by using a dynamic-programming-based join enumerator that explores bushy join trees — not just left-deep. This is a subset of the DPccp (Dynamic Programming connected complement pairs) algorithm adapted to goopg's v0 planner.

## Context

M0033-0002 proved that planner unnesting correctly eliminates the correlated subquery bottleneck in Q2. However, Q2 at SF=1 still exhausts memory because the outer query's 5-table comma-join contains a CROSS join producing 2 billion intermediate rows (documented in `analysis/tpch-unnesting-results.md`).

The CROSS join exists because the current planner builds only left-deep trees — no left-deep permutation of Q2's 5 tables can avoid placing `part` and `supplier` adjacent without a direct equality edge.

### Why DPccp

PostgreSQL uses the DPccp algorithm (`postgres/src/backend/optimizer/path/joinrels.c`, `make_rels_by_clauseless_joins` etc.) for join enumeration. DPccp operates on the join graph:
1. Decompose the graph into **connected subgraphs** (csg).
2. For each csg, find its **connected complement pairs** (cmp) — pairs of subgraphs that together form the csg and have at least one join edge between them.
3. Use dynamic programming: `best[S] = min over connected splits S = A ∪ B of (best[A] ⋈ best[B])`.

This is a bushy-capable, optimal join-order algorithm for acyclic queries. For v0, we implement a simplified DP that:
- Enumerates all subsets of tables joined by equijoin edges.
- For each subset, splits into connected pairs and picks the optimal join.
- Falls back to left-deep when stats are unavailable or the graph is disconnected.

### Why not left-deep DP (Selinger)

Selinger-style DP explores all left-deep permutations (O(n!)) but still cannot produce bushy trees. Q2's join graph requires a bushy tree to avoid Cartesian products — no left-deep ordering can do it. Selinger DP would find the "best left-deep plan" which is still CROSS(part, supplier) = 2 × 10^9 rows.

## Required Design Docs

1. `docs/design/0034-0001-dp-bushy-join-enumeration.md` — Join graph construction, subset enumeration, connected-complement-pair splitting, DP recurrence, integration with predicate pushdown and join algorithm selection.

## Definition of Done

1. **Join graph construction**: `buildJoinGraph(fromTables, whereConjuncts)` returns an undirected graph with one node per FROM table and one edge per equijoin predicate. Each edge carries its left/right key expressions.

2. **DP enumeration**: `enumerateBushyPlans(graph, tableStats)` applies dynamic programming:
   - Iterates over all subsets of the graph nodes (tables), ordered by increasing subset size.
   - For each subset S, checks connectivity (if there is a path using only edges within S).
   - For each connected subset S, iterates over splits `S = A ∪ B` where A and B are both connected AND there exists at least one edge crossing between A and B.
   - `best[S] = argmin_{A,B} cost(best[A] ⋈ best[B])` where cost is estimated cardinality.
   - Singleton subsets use `SeqScan` with ANALYZE row count.
   - Disconnected subsets are skipped — they cannot form a bushy join.

3. **Graph connectivity pre-check**: A DFS walk verifies the entire join graph is connected. If not, connected components are joined by CROSS (the unavoidable residual Cartesian product). The DP runs per-component.

4. **Cost model**: `cost(plan) = EstimatedRows(plan)` — simple cardinality-based cost. Joins use upstream's formula `|A| × |B| / max(NDistinct(key), 1)`. No I/O cost or CPU cost in v0.

5. **DP search space bound**: For Q2's 5 tables: `2^5 = 32` subsets, at most ~50 splits. For 8 tables: 256 subsets, ~1,000 splits. For 10 tables: 1,024 subsets, ~5,000 splits — all trivially fast. The DP gracefully degrades to left-deep when the graph is large (≥12 tables) or stats are missing.

6. **Integration**: `planFromRangeVars` dispatches to the DP planner when all tables have ANALYZE statistics. Otherwise falls through to existing left-deep logic.

7. **Q2 plan**: Contains zero `JoinTypeCross` nodes in the outer query. All joins are `JoinAlgoHash` with non-nil `LeftKey`/`RightKey`.

8. **Regression**: All 22 TPC-H queries remain plannable. Non-Q2 queries unaffected when stats are absent.

9. **Q2 execution**: Q2 on partial SF=1 data (4M lineitem rows) completes with RSS ≤ 20 GiB GOMEMLIMIT and with result rows returned.

10. **Design doc accepted**: `docs/design/0034-0001-dp-bushy-join-enumeration.md` written and indexed.

## Reference

- `internal/planner/planner.go:528-592` — `planFromClause`, `planFromRangeVars`
- `internal/planner/pushdown.go:29-49` — `pushPredicatesIntoCrossJoins`
- `internal/planner/pushdown.go:163-186` — `collectEqualityEdges`
- `internal/planner/planner.go:610-656` — join algorithm selection
- `internal/planner/planner.go:918-958` — `splitEqualityForHash`
- `internal/planner/joincost.go` — `chooseInnerJoinAlgo`
- `internal/planner/joinorder.go:60-139` — `reorderCommaFromByCardinality`
- PostgreSQL reference: `postgres/src/backend/optimizer/path/joinrels.c` (DPccp implementation)
- `analysis/tpch-unnesting-results.md` — Q2 CROSS join bottleneck
