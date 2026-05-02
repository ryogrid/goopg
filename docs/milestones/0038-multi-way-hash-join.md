# Milestone 0038 — Multi-Way Hash Join

**Status:** planned
**Depends on:** M0037 (spill-to-disk infrastructure), M0036 (lazy hash join), M0034 (bushy DP plan), M0033 (subquery unnesting)
**Drives:** Eliminate intermediate join result accumulation across multiple join levels by joining N base tables in a single hash-table-based pass. This removes the per-level drainRows copy chain that causes Q2's 24.8 GB RSS.

## Context

M0037 reduced Q2 RSS from 30.9 GB to 24.8 GB by spilling intermediate rows to disk.
However, the fundamental bottleneck remains: each binary join level (4 levels in Q2's
bushy plan) accumulates the child's full output in drainRows. Even with spill-to-disk,
the data must be read back from disk and re-hashed at the next level.

The only way to break this chain is to join multiple tables **in a single operator**,
eliminating intermediate join results entirely:

```
Before (4 binary joins, 3 intermediate result sets):
  Join(Join(Join(Join(part, partsupp), supplier), nation), region)
  → 3 intermediate result sets of ~800K rows each

After (1 multi-way join + 1 binary join):
  MultiHashJoin(partsupp, supplier, nation, region)
  → 0 intermediate result sets
  Join(result, part)
  → 1 intermediate result set
```

### How multi-way hash join works for Q2

Build hash tables from the small dimension tables, then probe the large fact table:

```
Probe: SeqScan(partsupp) → 800K rows
  For each row:
    s = HT_supplier[ps_suppkey]           → get supplier row
    n = HT_nation[s.s_nationkey]          → get nation row  
    r = HT_region[n.n_regionkey]          → get region row
    if r.r_name = 'EUROPE':
      output concat(partsupp row, s, n, r)
```

Three hash lookups per partsupp row. No intermediate join results. Memory = 3 small
hash tables (~10K + 25 + 5 rows) + 1 output row at a time (lazy). Peak RSS: buffer
pool (2 GB) + 3 hash tables (~10 MB) + query scratch (~100 MB) = **~2-3 GB**.

## Required Design Docs

1. `docs/design/0038-0001-multi-way-hash-join.md` — MultiHashJoin operator: build
   phase (N hash tables from small tables), probe phase (stream from fact table),
   chain-lookup semantics, output schema merging, integration with bushy planner.

## Definition of Done

1. **`MultiHashJoin` operator**: New executor operator implementing `Operator`.
   - `Open()`: Opens all N children. For each "build" child (small tables), drains
     rows and builds a hash table keyed by the equijoin column. One "probe" child
     (fact table) is streamed.
   - `Next()`: Pulls one probe row, chains through hash tables via equijoin keys,
     emits `concatRows(probe, build1, build2, ...)`. Lazy output (one row at a time).
   - `Close()`: Cleans up all state.
   - Schema: concatenation of all children's schemas.

2. **`Build()` dispatch**: `executor.Build` recognizes `*planner.MultiHashJoin` plan
   node and constructs the operator.

3. **Planner `MultiHashJoin` node**: New plan node type with fields:
   - `Tables []Node` — N child plan nodes (SeqScans or simpler)
   - `Keys  []MultiHashKey` — per-edge: `{LeftTable int, LeftCol, RightTable int, RightCol}`
   - `ProbeTable int` — which child is the probe (fact table)
   - `Filters []Expr` — residual filters (r_name = 'EUROPE', p_size = 15, etc.)

4. **Planner detection**: `planSelect` detects when the bushy DP produces a chain
   of N sequential hash joins where the intermediate tables form a "star" or
   "chain" shape. Rewrites the chain into a `MultiHashJoin` node.
   - Q2's bushy plan: `supplier ⋈ nation ⋈ region` is a 3-table chain → rewritten.
   - `partsupp` joins the chain result → added as probe.
   - `part` remains as separate binary join.

5. **Q2 integration**: The Q2 plan after multi-way hash join has:
   - `MultiHashJoin(partsupp, supplier, nation, region)` — 4 tables, 3 hash lookups
   - `Join(result, part) ON p_partkey = ps_partkey` — 1 binary join
   - `Aggregate(min(ps_supplycost) GROUP BY ps_partkey)` — unnest subquery

6. **Memory measurement**: Q2 on partial SF=1 data with `shared_buffers=2048MB`
   + `GOMEMLIMIT=20GiB` completes with peak RSS ≤ 10 GB.

7. **Performance**: Q2 execution time ≤ 120s.

8. **Regression**: All 22 TPC-H queries build and execute. Existing binary joins
   unaffected (multi-way only fires when the bushy DP finds a chain pattern).

## Reference

- `internal/executor/operators_join_agg.go` — current hash join operator
- `internal/executor/operators_join_agg.go:40-75` — `joinOp.Open()` pattern
- `internal/executor/operator.go:9-29` — `Operator` interface
- `internal/planner/bushy.go` — bushy DP plan construction (chain detection entry point)
- `internal/planner/plan.go` — plan node types
- `analysis/tpch-spill-hash-join-results.md` — Q2 24.8 GB RSS, intermediate join accumulation
- `analysis/tpch-final-run-004.md` — Q2 plan shape documented
