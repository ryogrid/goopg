# Milestone 0034 — Bushy Join Tree / Join-Graph Optimization

**Status:** planned
**Depends on:** Milestone 0033 (subquery unnesting confirmed; CROSS join identified as remaining bottleneck), Milestone 0006 (planner statistics for cardinality estimates)
**Drives:** Eliminate the CROSS join explosion in Q2's outer 5-table comma-join by building bushy join trees instead of left-deep-only chains. This is the prerequisite for Q2 to execute at SF=1 without unbounded memory growth.

## Context

M0033-0002 proved that the planner unnesting correctly eliminates the correlated
subquery bottleneck in Q2 — the subquery now runs once instead of per outer row.
However, Q2 at SF=1 still exhausts memory (30 GB RSS) because the outer query's
5-table comma-join contains a CROSS join that produces 2 billion intermediate rows.

### Why the CROSS join exists

The current planner (`planFromRangeVars` → left-deep CROSS chain →
`pushPredicatesIntoCrossJoins`) can only promote a CROSS join to INNER when an
equality spans the left and right children of a specific Join node in the
left-deep chain. With Q2's table order:

```
part ──p_partkey=ps_partkey──→ partsupp ←──s_suppkey=ps_suppkey── supplier
                                                                    │
                                                         s_nationkey=n_nationkey
                                                                    │
                                                                nation
                                                                    │
                                                         n_regionkey=r_regionkey
                                                                    │
                                                                region
```

Any left-deep ordering of these 5 tables necessarily places `part` and `supplier`
adjacent in the join chain at some point — but there is no direct equality
between them. The resulting CROSS join produces `|part| × |supplier| = 2 × 10^9`
intermediate rows.

### How bushy join trees fix this

A bushy join tree can group tables by their equijoin connectivity independently:

```
HashJoin(p_partkey = ps_partkey)
├── SeqScan(part)
└── HashJoin(s_suppkey = ps_suppkey)
    ├── HashJoin(s_nationkey = n_nationkey)
    │   ├── HashJoin(n_regionkey = r_regionkey)
    │   │   ├── SeqScan(nation)
    │   │   └── SeqScan(region)
    │   └── SeqScan(supplier)
    └── SeqScan(partsupp)
```

Every join is INNER with an equijoin key — no CROSS joins. The intermediate
row counts are bounded by the actual join selectivities, not by Cartesian
products.

## Required Design Docs

1. `docs/design/0034-0001-bushy-join-planning.md` — Join graph construction from WHERE
   equijoins, connected-component grouping, greedy bushy tree assembly, integration
   with predicate pushdown and join algorithm selection.

## Definition of Done

1. **Join graph construction**: `buildJoinGraph(fromTables, wherePredicate)` returns
   an undirected graph where nodes are FROM tables and edges are equijoin predicates
   (extracted from the WHERE clause). Edge weight = estimated join cardinality
   (from ANALYZE stats, or just count of connected tables).

2. **Bushy tree assembly**: Starting from the smallest table (by ANALYZE row count),
   greedily join the next table that shares an equijoin edge with any already-joined
   table. This builds a single connected component. Repeat for disconnected components.
   Finally, CROSS-join the components in order of increasing size.

3. **Integration**: `planFromRangeVars` dispatches to the bushy planner when
   all tables have ANALYZE statistics and there are ≥3 tables in the FROM clause
   (the existing reorder pass already gates on these conditions).

4. **Q2-specific verification**: The Q2 plan tree contains no CROSS joins. All joins
   in the outer query's plan tree are `JoinAlgoHash` or `JoinAlgoMerge` with non-nil
   `LeftKey`/`RightKey`.

5. **Regression test**: All 22 TPC-H queries remain plannable. Non-Q2 queries
   are unaffected (the bushy planner falls back to left-deep when stats are absent).

6. **Q2 execution**: Q2 on partial SF=1 data (4M lineitem rows) completes without
   RSS exceeding the 20 GiB GOMEMLIMIT, and results are returned.

7. **Performance**: Q2 execution time is bounded (completes within a few minutes
   rather than timing out at 5+ minutes).

8. **Design doc accepted**: `docs/design/0034-0001-bushy-join-planning.md` written
   and indexed in `docs/design/README.md`.

## Reference

- `internal/planner/planner.go:528-592` — `planFromClause`, `planFromRangeVars` (left-deep CROSS chain builder)
- `internal/planner/pushdown.go:29-49` — `pushPredicatesIntoCrossJoins` (CROSS → INNER promotion)
- `internal/planner/pushdown.go:163-186` — `collectEqualityEdges` (edge collection from WHERE conjunctions)
- `internal/planner/joinorder.go:60-139` — `reorderCommaFromByCardinality` (existing reorder pass)
- `internal/planner/planner.go:610-656` — Join algorithm selection (hash/merge)
- `internal/planner/planner.go:918-958` — `splitEqualityForHash` (disjoint-side equality decomposition)
- `internal/planner/joincost.go` — cost-driven algorithm selection
- `analysis/tpch-unnesting-results.md` — Q2 CROSS join bottleneck documentation
