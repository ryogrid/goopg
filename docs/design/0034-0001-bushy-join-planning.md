# 0034-0001 — Bushy Join Tree Planning

**Status:** draft
**Parent milestone:** M0034
**Date:** 2026-05-02

## 1. Objective

Replace the current left-deep-only CROSS join chain builder in `planFromRangeVars`
with a join-graph-based approach that builds **bushy join trees**. This eliminates
the CROSS join explosion that occurs when the FROM-list order places two tables
adjacent in the chain that share no equality predicate (e.g. `part` and `supplier`
in Q2).

## 2. Current Behaviour

### 2.1 Left-deep CROSS chain

`planFromRangeVars` (planner.go:565) iterates the FROM list and wraps each new
table with a CROSS join on the left:

```go
root = &Join{Type: JoinTypeCross, Left: root, Right: n}
```

Result: `CROSS(CROSS(CROSS(CROSS(t0, t1), t2), t3), t4)`

### 2.2 Predicate pushdown

`pushPredicatesIntoCrossJoins` splits the WHERE conjunction and pushes each
conjunct onto the deepest CROSS join whose schema spans both sides. A conjunct
lands on a join when:
1. The join is still `JoinTypeCross`.
2. The conjunct's `ColumnRef` indices span both the left and right children.

After pushdown, some CROSS joins become INNER + HASH. Conjuncts that don't span
any remaining CROSS join (including those that reference already-promoted joins)
stay on the outer Filter.

### 2.3 Join-order reordering

`reorderCommaFromByCardinality` (joinorder.go:60) permutes the FROM list to
place small tables first, using greedy cardinality preference with edge
connectivity. This helps but does not eliminate CROSS joins entirely — the
join tree is still left-deep, so two tables without a direct edge end up
adjacent in the chain and produce a CROSS join.

### 2.4 Why this fails for Q2

Q2's 5 tables: `part(200K)`, `supplier(10K)`, `partsupp(800K)`, `nation(25)`,
`region(5)`.

Equality edges:
```
part ──p_partkey=ps_partkey──→ partsupp ←──s_suppkey=ps_suppkey── supplier ──s_nationkey=n_nationkey──→ nation ──n_regionkey=r_regionkey──→ region
```

Any left-deep ordering puts `part` and `supplier` adjacent at some level,
producing `200K × 10K = 2 × 10^9` intermediate rows.

## 3. Proposed Algorithm

### 3.1 Join graph construction

Build an undirected graph from the WHERE clause:

```
type joinEdge struct {
    leftTable  int       // index into FROM list
    rightTable int
    predicate  Expr      // the equality expression
    leftKey    Expr      // for hash join
    rightKey   Expr
}
```

Edges are collected from the WHERE conjunction by matching `=` predicates
where the LHS and RHS `ColumnRef` indices fall in different FROM tables.

### 3.2 Connected-component grouping

Run a union-find or DFS to partition the tables into connected components.
Tables with no incident edges form singleton components.

Q2 connectivity: all 5 tables are in one connected component (part connected
to partsupp via p_partkey=ps_partkey; partsupp connected to supplier via
s_suppkey=ps_suppkey; supplier connected to nation via s_nationkey=n_nationkey;
nation connected to region via n_regionkey=r_regionkey).

### 3.3 Greedy bushy tree assembly (per component)

For each connected component:

1. **Pick the smallest table** by ANALYZE `RowCount` as the seed.
2. **Select the next table** that has an edge to any already-joined table,
   preferring the one with the smallest estimated join cardinality
   (`|joined_rows| × |next| / max(NDistinct(key_col), 1)`).
3. **Create a `Join` node** between the current plan and the new table,
   setting `Type=JoinTypeInner`, `Algo=JoinAlgoHash`, `LeftKey`/`RightKey`
   from the edge predicate.
4. Repeat until all tables in the component are joined.

This produces a bushy tree where joins are always along equijoin edges.

### 3.4 Component merge

For disconnected components (if any): CROSS-join components in increasing
order of estimated total size. This is the residual Cartesian join that
cannot be avoided because no edge connects the components.

For Q2, all tables are in one component, so no CROSS joins remain.

### 3.5 Integration with pushdown and join algorithm selection

After the bushy tree is built:
1. Walk all remaining `JoinTypeCross` nodes and attempt to push conjuncts
   from the outer Filter (same as existing pushdown).
2. For each `JoinTypeInner` with a predicate, call `splitEqualityForHash`
   to set `Algo=JoinAlgoHash` and `LeftKey`/`RightKey`.
3. For INNER joins with stats, call `chooseInnerJoinAlgo` for cost-driven
   algorithm selection.
4. The outer Filter retains only conjuncts that aren't INNER join predicates.

## 4. Implementation Plan

### 4.1 New file: `internal/planner/bushy.go`

| Function | Responsibility |
|----------|---------------|
| `buildJoinGraph(tables []*catalog.Table, conjuncts []Expr, leftWidths []int) (*joinGraph, error)` | Collects equijoin edges from WHERE conjuncts. Returns graph with nodes = table indices and edges = equijoin predicates. |
| `connectedComponents(g *joinGraph) [][]int` | Partitions tables into connected components via DFS. |
| `buildBushyComponent(tables []*catalog.Table, comp []int, edges []joinEdge, cat catalog.Catalog) (Node, error)` | Greedy bushy tree assembly for one component. Starts from smallest table, greedily picks next by edge connectivity + estimated cardinality. |
| `buildBushyJoinTree(tables []*catalog.Table, conjuncts []Expr, scanNodes []Node, cat catalog.Catalog) (Node, []Expr, error)` | Top-level entry point. Calls graph construction, components, per-component assembly, component merge. Returns the bushy plan tree and residual conjuncts. |

### 4.2 Changes to `planFromClause` / `planFromRangeVars` (planner.go)

In `planFromClause`, after building scan nodes for all FROM tables, check
whether to use the bushy planner:
- Condition: all tables have ANALYZE stats (same gate as `reorderCommaFromByCardinality`).
- If yes: call `buildBushyJoinTree` instead of the left-deep CROSS chain.
- If no: fall through to existing left-deep logic.

### 4.3 Integration with `pushPredicatesIntoCrossJoins`

After the bushy tree is built, residual conjuncts that were NOT used as
join edges still need to be pushed. The existing `pushPredicatesIntoCrossJoins`
can handle this — pass the bushy tree through it.

Alternatively, `buildBushyJoinTree` returns both the plan and the residual
conjuncts. The caller wraps the plan in a Filter with the residuals.

### 4.4 Limitations (v0)

- Edge detection uses only `=` predicates between column references from
  different tables. Complex join conditions (`a.x + 1 = b.y`) are deferred.
- Multi-column join keys are not yet supported (single-column edges only).
- Cycle handling: when the join graph has cycles (multiple paths between
  the same tables), the greedy algorithm picks one edge path. The other
  edges become residual filter conjuncts.
- Outer joins (LEFT/RIGHT/FULL) are NOT handled by the bushy planner.
  Tables with explicit `JOIN ... ON` clauses remain on the left-deep path.
- The bushy planner requires ANALYZE statistics on all tables.

## 5. Verification

### 5.1 Unit tests

- `TestBushyQ2NoCrossJoins`: Q2 plan contains zero `JoinTypeCross` nodes.
- `TestBushyQ2AllHashJoins`: All joins in Q2 plan have `Algo=JoinAlgoHash`.
- `TestBushyPreservesResults`: Q2 results from bushy plan match original
  left-deep plan (on sample data).
- `TestBushyTwoComponents`: Two disconnected groups produce one CROSS join
  between components.
- `TestBushyFallbackWithoutStats`: Left-deep plan is used when stats missing.
- `TestBushyRegression22Queries`: All 22 TPC-H queries plan without error.

### 5.2 Integration tests

- `TestBushyQ2PartialSF1`: Q2 on 4M lineitem data completes in < 120s
  without RSS exceeding 20 GiB.
- `TestBushyQ5NoCrossJoins`: Q5 (6-table join) plan contains no cross joins.

## 6. Reference

- `internal/planner/planner.go:565-592` — `planFromRangeVars` (left-deep CROSS chain)
- `internal/planner/pushdown.go:1-49` — predicate pushdown for CROSS joins
- `internal/planner/joinorder.go:60-139` — `reorderCommaFromByCardinality`
- `internal/planner/pushdown.go:163-186` — `collectEqualityEdges` (edge collection from WHERE)
- `internal/planner/pushdown.go:144-174` — `walkColumnRefs` (expression tree walker)
- `internal/planner/pushdown.go:108-138` — `classifyConjunctSide` (side classification)
- `internal/planner/planner.go:918-958` — `splitEqualityForHash`
- `internal/planner/planner.go:610-656` — join algo selection + Join construction
- `analysis/tpch-unnesting-results.md` — Q2 CROSS join bottleneck documented in M0033-0002
