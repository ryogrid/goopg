# M0146-0005 (part 4): slices 56+ — merge-join costing

Continuation of [m0146-0005-join-order-burndown-3.md](m0146-0005-join-order-burndown-3.md)
(slices 38-55), split per the design-doc size rule (D3). Same task and census
family.

## Slice 56: M0146-0005bd — a merge join is priced by final_cost_mergejoin

TPC-DS Q47/Q57 join three references to the `v1` CTE. PG 18.3 builds
`v1_lead ⋈ (v1_lag ⋈ Materialize(v1))` and puts a `Materialize` above
every merge inner that is a CTE Scan. goopg joined `v1_lag ⋈ v1_lead`
first and printed no Material. `mergeJoinCost` was a
"milestone-fidelity" formula: one comparison per input row whatever the
clause count, no rescan term, and no materialize-inner decision (the
header of joinpathsmergeouter.go said so and ledgered it).

- `mergeJoinCost` (cost\_funcs.go) is now `final_cost_mergejoin`:
  - the merge clauses cost their `cost_qual_eval` operator count on every
    tuple read from either side;
  - duplicate inner groups are re-fetched,
    `rescanratio = 1 + max(0, mergejointuples − inner rows) / inner rows`;
  - `materialize_inner` is elected as PG does: never under
    `skip_mark_restore`; when buffering is cheaper than re-running; when a
    presorted inner cannot mark/restore (`execSupportsMarkRestore`: only
    btree index scans, Material and Sort can); or when an explicitly
    sorted inner exceeds work\_mem.
- An elected presorted inner is wrapped in a PathMaterial priced as
  `create_mergejoin_plan` prices it (the inner plus `cpu_operator_cost`
  per row), so EXPLAIN shows the `Materialize` PG shows.
- `mergejointuples` is `approx_tuple_count` over the path's merge clauses
  (outer rows × inner rows × each clause's selectivity). It used to be
  recovered as `joinrel.Rows / sel(residual)`. With the joinrel clamped to
  one row, Q47's `v1_lag ⋈ v1` then emitted 200 tuples where PG counts 1,
  and the new rescan term priced 199 phantom inner rescans. With that bug
  the first cut elected a nested loop over the `v1_lag ⋈ v1_lead`
  underestimate, and Q47/Q57 timed out in the sweep.
- `extra->inner_unique` reaches the merge arm: false for SEMI and ANTI,
  true for a unique-ified inner, else `innerRelProvenUnique`. A unique
  inner whose join clauses are all merge clauses sets `skip_mark_restore`.
  `innerRelProvenUnique` gains the GROUP BY/DISTINCT arm of
  `query_is_distinct_for` (`groupedLeafDistinctFor`): a grouped leaf is
  unique when every grouping column is equated, tracked by position
  through Project, subquery and inlined-CTE scans, Filter, Sort, Limit,
  Material and a WindowAgg's input columns. TPC-DS Q83's per-channel
  `GROUP BY i_item_id` inners and Q77's grouped Merge Left Join inners
  would otherwise gain a Material PG does not print. The proof also feeds
  the hash and nested-loop inner-unique factors.

Tests:

- `TestExplainMergeJoinMaterializesCTEInner` pins PG 18.3's
  `Materialize (cost=0.00..4.50)` and the 11.50 merge run cost. It fails
  on base.
- `TestMergeJoinCostMaterializeElection` covers the four arms.
- `TestGroupedLeafDistinctFor` covers the grouping-column proof through a
  renaming Project.
- `TestMergeJoinTuplesIsApproxTupleCount` replaces the helper test.
- The truncation test looks through the Material its rescanned index
  inner now gets.

Movement (fire-set gate, both TPC-DS scales; match counts unchanged at
26 and 22):

| Category (excl. match) | SF0.25 | SF1 |
|---|---|---|
| join-order | 57 → 56 | 63 → 62 |
| join-method | 34 → 31 | 32 → 30 |
| scan-type | 39 → 37 | 44 → 42 |
| aggregation-strategy | 24 → 22 | 24 → 23 |
| parallelism | 38 → 37 | 49 → 48 |
| qual-placement | 12 → 14 | 10 → 11 |

- Q47 and Q57 take PG's join order.
- Q58 loses its join-order, join-method and aggregation records.
- Q64 and Q77 lose join-method.
- The qual-placement rises are deeper records that are now reachable
  (Q58, Q64).

The Q47/Q57/Q58/Q64/Q65/Q77 fire set executes at both scales. TPC-H plans
are byte-identical (TPC-H plans have no merge joins). In the regress
runner, join.sql, partition\_join.sql and subselect.sql move among merge
plans that match PG in neither version (PG removes the join, or joins
partitionwise). Evidence: `analysis/m0146/m0146-0005/slice56/`.

Ledgered:

- A Material PG elects above an explicitly sorted inner is priced but not
  emitted (the merge plan absorbs its Sort child).
- goopg's merge executor still buffers each inner group itself, so a
  Material inner is buffered twice at run time.
- Merge keys are matched by column expression, not equivalence class.
  Q47's top merge therefore sorts the `v1_lag ⋈ v1` inner that PG
  materializes presorted.
- Grouped subquery leaves carry no pathkeys into the join search, so a
  merge over them sorts both sides.
- EXPLAIN prints a merge path's residual equality inside `Merge Cond`
  rather than `Join Filter`.
- `groupedLeafDistinctFor` does not check PG's `hasTargetSRFs` guard.
