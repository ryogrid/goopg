# 0034-0001 — DP-Based Bushy Join Enumeration (DPccp-Style)

**Status:** superseded
**Superseded by:** [leftdeep-joins/](leftdeep-joins/) — the PG-shaped DP search replaces this DPccp-style enumerator; both the old subset-bitmask DP code and its design are retired (M0127-P6.3).
**Parent milestone:** M0034
**Date:** 2026-05-02

## 1. Objective

Replace the current left-deep-only CROSS join chain builder in `planFromRangeVars`
with a dynamic-programming-based enumerator that explores **bushy join trees**.
The DP enumerates connected subsets of the join graph, splits each into connected
complement pairs, and selects the optimal join order by estimated cardinality.
This is a simplified adaptation of PostgreSQL's DPccp algorithm
(`postgres/src/backend/optimizer/path/joinrels.c`).

## 2. Current Behaviour

The FROM clause builds a left-deep CROSS join chain (`CROSS(CROSS(CROSS(...)))`).
`pushPredicatesIntoCrossJoins` promotes CROSS to INNER where an equality spans
both sides of a join node. This is left-deep only — if two tables without a
direct edge become adjacent in the chain, they form a CROSS join.

For Q2: `part` and `supplier` lack a direct equality, so `CROSS(part, supplier) =
200K × 10K = 2 × 10^9` intermediate rows. No left-deep permutation avoids this.

## 3. DPccp Algorithm (Simplified for v0)

### 3.1 Join graph

Nodes: FROM tables (0..n-1).
Edges: equality predicates `t_i.col = t_j.col` from WHERE conjunctions.

```
type joinGraph struct {
    nodes int
    edges []joinEdge  // each edge: {leftTable, rightTable, predicate, leftKey, rightKey}
    adj   [][]int      // adjacency list: adj[i] = list of edge indices incident to node i
}
```

### 3.2 Connectivity check

A DFS from any node checks whether the entire graph is connected. If not,
connected components are identified. The DP runs per-component. Components
are CROSS-joined after DP (unavoidable residual Cartesian product).

Q2's graph: all 5 nodes connected via `part—partsupp—supplier—nation—region`.
Single component → DP covers the full graph.

### 3.3 Subset enumeration

For a component with `k` nodes, enumerate all 2^k subsets. Represent each subset
as a bitmask `uint16` (supports up to 16 tables, sufficient for v0). Iterate
subsets in order of increasing popcount (size).

```
for size := 1; size <= k; size++ {
    for each subset S where popcount(S) == size {
        // Process S
    }
}
```

### 3.4 Connected-subset check

A subset S is **connected** if the subgraph induced by S (edges whose both
endpoints are in S) has a path between every pair of nodes. Use a BFS/DFS
within the subset.

Only connected subsets are eligible for DP — disconnected ones cannot be
joined without a Cartesian product.

### 3.5 Complement-pair split

For each connected subset S (|S| ≥ 2), enumerate connected splits:
- Find all subsets A ⊂ S where A is non-empty and connected.
- B = S \ A.
- Check that B is also connected AND there exists at least one edge
  between a node in A and a node in B (join edge). Without this edge,
  joining A and B would be a Cartesian product.
- If both conditions hold, (A, B) is a valid complement pair.

```
for each A ⊂ S where A connected:
    B = S \ A
    if B connected and hasCrossEdge(A, B, graph):
        plan = best[A] ⋈ best[B]
        if cost(plan) < cost(best[S]):
            best[S] = plan
```

### 3.6 DP state

```
best[mask] = Plan  // optimal bushy plan for the subset of tables in mask
```

Singleton: `best[1<<i] = SeqScan(table[i])`, cost = ANALYZE row count.

Join: `cost(A ⋈ B) = |A| × |B| / max(NDistinct(join_key), 1)`

The DP result is `best[(1<<k)-1]` — the optimal bushy plan for the entire component.

### 3.7 Cost comparison

The DP compares splits by estimated total cardinality of the join result.
Ties are broken by preferring smaller left subtrees (heuristic: smaller
build side for hash join).

### 3.8 Fallback to left-deep

When ANALYZE stats are absent on any table in the component, the DP is skipped
and the existing left-deep logic runs instead. When the component has ≥ 12
tables (2^12 = 4,096 subsets, enumeration still fast but O(3^k) splits become
excessive), also fall back.

## 4. Implementation Plan

### 4.1 New file: `internal/planner/bushy.go`

| Type/Function | Description |
|---------------|-------------|
| `type joinGraph struct` | Nodes count, edge list, adjacency list |
| `type joinEdge struct` | Left/right table index, predicate Expr, left/right key Expr |
| `type dpEntry struct` | Plan Node for a subset, estimated cardinality int64, schema Schema |
| `buildJoinGraph(tables []*catalog.Table, conjuncts []Expr) (*joinGraph, error)` | Extract equijoin edges from WHERE conjunctions |
| `enumerateBushyPlans(g *joinGraph, scans []Node, stats []*catalog.TableStats) (Node, []Expr, error)` | DPccp entry point: per-component enumeration, returns best bushy plan + residual conjuncts |
| `isConnected(mask uint16, g *joinGraph) bool` | BFS within subset |
| `hasCrossEdge(a, b uint16, g *joinGraph) bool` | Check existence of ≥1 edge between A and B |
| `estimateJoinSize(leftRows, rightRows int64, edge joinEdge, stats []*catalog.TableStats) int64` | Cardinality estimate for equijoin |

### 4.2 Changes to `planFromClause` / `planFromRangeVars` (planner.go)

After building `SeqScan` nodes for each FROM table, check the gate:
- All tables have `Table.Stats != nil && Table.Stats.RowCount > 0`.
- Total tables ≤ 12.

If gate passes:
1. Build join graph from WHERE conjuncts.
2. Call `enumerateBushyPlans()`.
3. Wrap in Filter with residual conjuncts.
4. Return bushy plan.

If gate fails: fall through to existing left-deep logic.

### 4.3 Integration with pushdown

The bushy DP uses equijoin edges directly — these predicates become `Join.Predicate`
/ `Join.LeftKey` / `Join.RightKey` on the constructed Join nodes. The residual
conjuncts (filters that don't form edges, subqueries, LIKE patterns, etc.) stay
in the outer Filter. The existing `pushPredicatesIntoCrossJoins` is not needed
for the bushy plan (there are no CROSS joins to push into).

### 4.4 Complexity bounds

| Tables (k) | Subsets (2^k) | Splits per subset | Total splits | Feasible? |
|------------|--------------|-------------------|-------------|-----------|
| 5 (Q2) | 32 | ≤15 | ~100 | Instant |
| 8 | 256 | ≤128 | ~5,000 | Instant |
| 10 | 1,024 | ≤512 | ~50,000 | ~1ms |
| 12 | 4,096 | ≤2,048 | ~500,000 | ~10ms |
| ≥13 | fallback to left-deep | — | — | — |

### 4.5 Q2's DP search space

Q2 has 5 tables, all in one connected component. The DP will:
1. Try singleton subsets (5 entries, trivial).
2. Try size-2 subsets: all pairs with an edge — (part, partsupp), (partsupp, supplier),
   (supplier, nation), (nation, region). 4 entries.
3. Try size-3 subsets: connected triples. E.g., {part, partsupp, supplier} can split
   as {part, partsupp} ⋈ {supplier} or {part} ⋈ {partsupp, supplier}. ~8 entries.
4. Try size-4 subsets. ~6 entries.
5. Try size-5 subset (all tables). ~5 splits.

**~25 DP entries total.** Trivial.

The optimal bushy plan found will be:
```
Join(p_partkey = ps_partkey)
├── SeqScan(part)
└── Join(s_suppkey = ps_suppkey)
    ├── Join(s_nationkey = n_nationkey)
    │   ├── Join(n_regionkey = r_regionkey)
    │   │   ├── SeqScan(region)
    │   │   └── SeqScan(nation)
    │   └── SeqScan(supplier)
    └── SeqScan(partsupp)
```

Zero CROSS joins. All joins are INNER HASH with equijoin keys.

## 5. Verification

### 5.1 Unit tests

- `TestJoinGraphQ2`: 5 nodes, 4 edges extracted from Q2's WHERE.
- `TestJoinGraphConnected`: Single connected component.
- `TestDPEnumerateQ2`: DP finds bushy plan, zero CROSS joins.
- `TestDPOptimalOrderQ2`: Cardinality-optimal plan has part and supplier
  on opposite subtrees (no CROSS join).
- `TestDPTwoComponents`: Two disconnected components → CROSS join between them.
- `TestDPFallbackWithoutStats`: Left-deep plan when stats missing.
- `TestDPFallbackLargeGraph`: Left-deep plan when ≥13 tables.
- `TestDPRegression22Queries`: All 22 TPC-H queries plan without error.

### 5.2 Integration tests

- `TestDPQ2PartialSF1`: Q2 on 4M lineitem data completes in < 120s,
  RSS ≤ 20 GiB, results returned.
- `TestDPQ5Bushy`: Q5 (6-table join) plan contains zero CROSS joins.
- `TestDPQ2Correctness`: Q2 results match PostgreSQL reference (sample data).

## 6. Reference

- `internal/planner/planner.go:565-592` — `planFromRangeVars`
- `internal/planner/pushdown.go` — predicate pushdown, `walkColumnRefs`, `splitAnd`
- `internal/planner/pushdown.go:163-186` — `collectEqualityEdges`
- `internal/planner/joinorder.go:60-139` — `reorderCommaFromByCardinality`
- `internal/planner/planner.go:918-958` — `splitEqualityForHash`
- `internal/planner/planner.go:610-656` — Join construction + algo selection
- `internal/planner/joincost.go` — cost-driven algorithm selection
- PostgreSQL: `postgres/src/backend/optimizer/path/joinrels.c` — DPccp reference
- PostgreSQL: `postgres/src/backend/optimizer/geqo/geqo_main.c` — genetic algorithm for large joins (≥12 tables)
- `analysis/tpch-unnesting-results.md` — Q2 CROSS join bottleneck
