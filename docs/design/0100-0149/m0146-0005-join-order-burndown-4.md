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

## Slice 57: M0146-0005be — a merge join keys on its path's mergeclauses

PG's merge path merges on the clauses its inputs' ordering serves
(`find_mergeclauses_for_outer_pathkeys`, then the truncation search). Every
other equality between the two sides stays in `joinqual` and prints as
`Join Filter:`. TPC-DS Q47/Q57 show this:

```
Merge Cond: ((v1_lag.i_category = v1.i_category) AND … )   -- 4 EC keys
Join Filter: (v1.rn = (v1_lag.rn + 1))
```

goopg's path carried the same four clauses, with the `rn` equality as
residual. The late `fillJoinHashKeys` pass then re-derived the key list
from the predicate and published every equality as a merge key. EXPLAIN
printed six `Merge Cond` pairs, and the executor sorted on all of them.

- `Join.MergeKeyCount` records the path's mergeclauses count in
  `createMergeJoinPlan`. The predicate lists those pairs first.
- `fillOneJoinHashKeys` keeps that prefix for a merge join. The later
  equalities fall to `ExecMergeKeyPlan`'s residual, which is the executor's
  per-pair check and EXPLAIN's `Join Filter`.

The executor still sorts both inputs itself on the (now shorter) key list.
Rows are unchanged: the dropped equalities are evaluated per pair instead.

Test: `TestExplainMergeJoinResidualEqualityIsJoinFilter` pins PG 18.3's
`Merge Cond: (y.a = x.a)` / `Join Filter: (x.rn = (y.rn + 1))`. It fails
on base, which printed both pairs as Merge Cond. On that query the whole
plan now reads as PG's: join order, Materialize inner, and the run cost
within 2%.

Movement: qual-placement −2 at both TPC-DS scales (SF0.25 14 → 12,
SF1 11 → 9); only Q47 and Q57 move, and both execute. TPC-H plans are
byte-identical. The regress runner (8 cases) shows only the known join.sql
row-order flap. Evidence: `analysis/m0146/m0146-0005/slice57/`. This
resolves slice 56's ledgered "residual equality inside Merge Cond" row.

## Slice 58: M0146-0005bf — a parallel DISTINCT stands on the cheapest partial path

TPC-DS Q38/Q87 dedup each set-operation branch with a parallel DISTINCT.
goopg printed `Sort (cost=8471.30..)` over its partial `Nested Loop
(cost=..18260.63)`, which is impossible: a Sort's startup cost includes
its input's total. The partial DISTINCT arm (`addPartialDistinctPaths`)
priced its per-worker input as the serial input's run cost divided by the
parallel divisor (`parallelSeedCost`, an acknowledged approximation). The
node it ran over was the search's own partial path, whose per-worker cost
the search had already computed. The grouped-aggregate split stopped
approximating in M0146-0005ap; the DISTINCT arm never followed.

- `createDistinctPaths` passes `searchedCheapestPartialInput`'s rebuilt
  input and partial path to the arm. Workers, per-worker rows and the seed
  cost come from that path, as `create_partial_distinct_paths` reads
  `input_rel->cheapest_partial_path` (planner.c:4897-4930). Without one,
  the arm keeps the old fallback.
- Priced honestly, the sorted arm (which sorts every input row per worker)
  stopped beating the serial plan on select\_distinct.sql's `SELECT
  DISTINCT four FROM tenk1`, which PG plans in parallel through its HASHED
  arm (planner.c:4983). goopg's arm had refused that one. It is now filed:
  `Unique -> Gather Merge -> Sort -> HashAggregate(partial)`. The per-worker
  node is a group-only hashed Aggregate over every output column, carrying
  the `PartialGroup` mark the parallel walks already descend through
  (M0146-0025). A dedup has no transition state, so the refusal's stated
  reason did not apply.

Tests:

- `TestPartialDistinctHashedArmLowers` pins the hashed arm's node chain
  down to a Parallel SeqScan.
- The sorted-arm tests run with `enable_hashagg` off so they keep
  inspecting that arm.
- On a scratch server, parallel and serial runs of multi-column DISTINCTs
  return identical results.

Movement: no TPC-DS category change. Q38/Q54/Q87 costs move to PG's
(Q87's Sort 18315.79 over the 18260.63 partial Nested Loop, PG 18352.37;
total 43057 against PG's 43325), and all three execute at both scales.
select\_distinct.sql's parallel case now matches PG 18.3 exactly (diff
102 → 97 lines). TPC-H plans are byte-identical. Evidence:
`analysis/m0146/m0146-0005/slice58/`.

Q87 still hashes its set operations where PG sorts them (`SetOp
Except`). The leader Unique estimates 355 rows where PG estimates 3260
(its estimate\_num\_groups keeps the input row count), and that estimate
drives the setop choice. Next.

Ledgered:

- EXPLAIN prints every Aggregate's cost through the legacy display
  derivation: `Aggregate` carries no PlanCost, so a path-chosen partial
  HashAggregate prints 2845.67 where its path costs 1949.84.
- The Q87 DISTINCT row estimate.
