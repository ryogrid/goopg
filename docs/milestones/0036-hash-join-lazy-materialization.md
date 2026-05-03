# Milestone 0036 — Hash Join Lazy Materialization (On-Demand Output)

**Status:** accepted
**Depends on:** M0035 (streaming hash join — probe side no longer drainRows'd), M0034 (DP bushy joins — zero CROSS joins), M0033 (subquery unnesting)
**Drives:** Eliminate the `o.rows` materialization in hash joins so that joined rows are yielded on demand via `Next()` rather than stored in memory during `Open()`. This removes the last remaining memory bottleneck in Q2 — the accumulation of all intermediate join results.

## Context

M0035 made the hash join **half-streaming**: the build side is drained into a hash
table, and the probe side streams one row at a time. However, every joined row is
still accumulated in `o.rows` during `Open()`. For Q2's 5-table join, this means:

- 800K rows from `part ⋈ partsupp` stored in `o.rows`
- Another 800K rows from `(part ⋈ partsupp) ⋈ supplier/nation/region` stored again
- Each row is a `concatRows` allocation → massive double-copy across join levels

The total `o.rows` memory across all join levels accounts for the majority of Q2's
30 GB peak RSS.

### Proposed fix

Instead of appending all matches to `o.rows` in `Open()`, store only the hash table
and a reference to the probe operator. During each `Next()` call:
1. Pull one row from the probe side.
2. Look up matches in the hash table.
3. Store the current list of matches and index within it.
4. Yield joined rows one match at a time per `Next()` call.

When matches for one probe row are exhausted, pull the next probe row.

This is **lazy materialization** — the join result never exists as a materialized
slice. Each `Next()` call produces one joined row on demand.

### Memory impact

| Component | Before (M0035) | After (M0036) |
|-----------|---------------|---------------|
| Build side (drainRows) | 400 MB (partsupp 800K) | Same — still needed |
| Hash table (map) | ~20 MB | Same |
| **o.rows** | **~1.4 GB** per join level | **0** (not stored) |
| Probe state | None | ~100 bytes (current row + match index) |
| **Peak per join** | **~1.8 GB** | **~420 MB** |

For 4 hash joins in Q2's plan, peak memory drops from ~7.2 GB (rows) to ~1.7 GB
(hash tables only). Combined with buffer pool arena (2 GB), expected peak RSS:
**~5-8 GB** (down from 30 GB).

## Required Design Docs

1. `docs/design/0036-0001-lazy-hash-join-materialization.md` — Lazy output model:
   hash table stored as operator state, probe operator reference, per-row match
   cursor, `Next()` dispatches probe → hash lookup → emit one match at a time.

## Definition of Done

1. **`joinOp` gains lazy-output state**: New fields: `probeOp Operator` (for dequeueing probe rows), `currentProbeRow Row`, `currentMatches []Row`, `matchIdx int`, `hashTable map[string][]Row`. `hasCurrent bool` tracks whether we're mid-probe-row.

2. **`Open()` changes**: Builds hash table from build side (drainRows). Opens probe operator. Sets `hasCurrent = false`. Does NOT accumulate `o.rows`.

3. **`Next()` changes**: If `hasCurrent && matchIdx < len(currentMatches)`, emit `concatRows(currentProbeRow, currentMatches[matchIdx])` and increment `matchIdx`. Else, pull next probe row via `probeOp.Next()`, look up hash table, set `currentMatches` and `matchIdx = 0`. On EOF from probe, return `EOF`.

4. **`Close()`**: Closes both child operators. Nils all state. Existing M0031 nil-on-Close pattern preserved.

5. **LEFT JOIN**: When probe row has no matches AND `plan.Type == JoinTypeLeft`, emit `concatRows(l, nullRight)` as a single match row. Set `currentMatches = nil` and `hasCurrent` appropriately.

6. **`BuildLeft` variant**: Symmetric — build left as hash table, probe right as streaming source.

7. **MERGE and NESTED-LOOP unchanged**: Continue to use full `o.rows` materialization (Nested-loop needs multiple passes; merge needs sorted streams).

8. **Regression tests**: All 22 TPC-H queries build and execute. `TestBuildTPCHQueries` and `TestPlanTPCHQueriesPlannable` pass.

9. **Memory measurement**: Q2 on partial SF=1 data with `shared_buffers=2048MB` + `GOMEMLIMIT=20GiB` completes with peak RSS substantially below the 30 GB baseline from M0035-0003. Target: ≤ 15 GB.

10. **Performance**: Q2 execution time bounded (completes within minutes rather than timing out at 300s).

## Reference

- `internal/executor/operators_join_agg.go:40-75` — `joinOp.Open()` (current streaming hash join)
- `internal/executor/operators_join_agg.go:87-152` — `runHashJoinStream` (current: appends to o.rows)
- `internal/executor/operators_join_agg.go:27-29` — `joinOp` struct fields
- `internal/executor/operators_join_agg.go:390-397` — `joinOp.Next()` (current: index into o.rows)
- `internal/executor/operator.go:9-29` — `Operator` interface
- `analysis/tpch-streaming-hash-join-results.md` — Q2 30 GB RSS bottleneck (o.rows accumulation)
