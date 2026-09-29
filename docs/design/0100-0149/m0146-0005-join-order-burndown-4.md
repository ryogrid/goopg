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

## Slice 59: M0146-0005bg — a DISTINCT node keeps its path's rows, and a parallel DISTINCT arm feeds the sorted SetOp

After slice 58, TPC-DS Q38/Q87 still built `HashSetOp` where PG 18.3
builds `SetOp Intersect` / `SetOp Except`. Their branch DISTINCTs printed
355 and 200 rows (PG: 3260), although the DISTINCT rel was sized at 3548.

- `Distinct` and `DistinctOn` embed `PlanCost`, so the `createPlanNode`
  funnel stamps the chosen path's cost and rows on them.
  `EstimateRows` reads a stamped, non-per-worker row count
  (`stampedUpperRows`), and EXPLAIN prints it. The lowered leader Unique
  reads a Gather Merge over a per-worker Unique. Re-estimating
  `estimate_num_groups` there lost the variables, so the default (200)
  or a partial clamp (355) took over. PG sizes the DISTINCT rel once, and
  every consumer reads that number.
- `setOpArmSortedAllCols` accepts a Unique over a Gather Merge whose merge
  keys are every output column. That is create\_partial\_distinct\_paths'
  shape, and it is as sorted as a Unique over a Sort. The SETOP\_SORTED
  candidate (M0146-0005q) is now offered over those arms and ties the
  hashed one on total while winning on startup, as in PG.
- EXPLAIN prints no `Sort Key:` under a sorted INTERSECT/EXCEPT. explain.c
  prints merge keys only for Merge Append.

Tests:

- `TestSetOpArmSortedThroughGatherMerge`.
- `TestEstimateRowsReadsStampedDistinctRows` (a per-worker stamp is
  ignored).
- `TestExplainSortedSetOpPrintsNoSortKey` (fails on base).

Movement:

- Q38/Q87's first divergence moves from depth 1 (HashSetOp vs SetOp) to
  depth 5/6 at both scales. The next record is PG's first branch sorting
  its Gather Merge input without a per-worker Unique.
- SF0.25 aggregation-strategy 22 → 21; other categories unchanged.
- The Q6/Q38/Q41/Q49/Q54/Q75/Q87 fire set executes at both scales.
- TPC-H plans are byte-identical, and the regress runner (6 cases) is
  unchanged.

Evidence: `analysis/m0146/m0146-0005/slice59/`. This resolves slice 58's
ledgered Q87 row-estimate row.

Ledgered: the sorted SetOp's printed startup is the legacy display's
larger child startup, where PG sums both inputs' startups. `SetOp`, like
`Aggregate`, carries no PlanCost.

## Slice 60: M0146-0005bh — a parallel DISTINCT may skip the per-worker dedup

After slice 59, Q38/Q87's first divergence was PG's store\_sales branch:
`Unique -> Gather Merge -> Sort -> <partial join>`, with no per-worker
Unique. PG's `create_distinct_paths` builds the final DISTINCT paths
over `input_rel` first. That includes the sorted Unique over the input
rel's Gather Merge of its sorted cheapest partial path, which
`generate_useful_gather_paths` files. Only after that does
`create_partial_distinct_paths` add the split arms. goopg filed only the
split arms (sorted per-worker Unique, and hashed per-worker dedup since
slice 58).

- `addPartialDistinctPaths` also files the unsplit arm: the leader Unique
  over a Gather Merge of the worker Sort, carrying every per-worker row
  (`perWorkerRows × d`). add\_path elects among the three.

Movement: TPC-DS SF1 PLAN-PARITY match 22 → 23 (Q87 = PG), with SF1
join-order 62 → 61, join-method 30 → 29 and sort-strategy 42 → 41.
SF0.25 categories are unchanged. There, Q38/Q87's first divergence (depth
5) is now the other branches, where PG keeps the per-worker Unique that
goopg drops. The Q38/Q87 fire set executes at both scales. TPC-H plans
are byte-identical. In the regress runner, select\_distinct.sql's `WHERE
four = 10` case loses one line of divergence (PG's LIMIT-1 partial arm is
still ledgered). Evidence: `analysis/m0146/m0146-0005/slice60/`.

Ledgered:

- goopg prices a Unique with `distinctCost`: a comparison per input row
  in both startup and total, plus `cpu_tuple_cost` per output row. PG's
  `create_upper_unique_path` keeps the subpath's startup and adds
  `cpu_operator_cost × rows × numCols` to the total (verified on Q87:
  19738.09 + 0.0025 × 3260 × 3 = 19762.54). Under PG's figures the split
  and unsplit arms differ by the per-worker Unique alone.
- Why PG keeps the split arm for Q38/Q87's catalog\_sales and web\_sales
  branches at SF0.25 is not yet explained; the two arms tie within
  add\_path's fuzz there.

## Slice 61: M0146-0005bi — a Unique is priced by create_upper_unique_path

Slice 60's ledger item. goopg priced every Unique with `distinctCost`: one
comparison per input row in both startup and total, plus `cpu_tuple_cost`
per output row. PG's `create_upper_unique_path` keeps the input's startup
(a Unique emits its first row as soon as the input does) and adds
`cpu_operator_cost × rows × numCols` to the total. TPC-DS Q87's leader
Unique in PG 18.3 checks out: `19738.09 + 0.0025 × 3260 × 3 = 19762.54`.

- `uniquePathCost` replaces `distinctCost` at all six Unique sites: the
  serial sorted DISTINCT, the per-worker Unique, the three leader Uniques
  over a Gather Merge, and UNION's Unique over a Merge Append. `numCols` is
  the sort-key count.

Tests: `TestUniquePathCostIsCreateUpperUniquePath` pins PG's Q87 figure.
`TestDistinctOrderByExpressionKeepsOuterSort`'s vacuity guard now also
recognises the sort-based Unique that can win the election; its assertion
already accepted either form.

Movement:

- No TPC-DS category change. Q87's Unique costs now follow PG's formula
  (branch 1: 19716.69 → 19742.10 over its Gather Merge; PG 19738.09 →
  19762.54).
- The fire set (Q6/Q38/Q41/Q49/Q54/Q87) executes at both scales, and
  TPC-H plans are byte-identical.
- Regress runner: union.sql moves toward PG (382 → 376 diff lines). In
  join.sql, the `select distinct id from j3` inner now plans PG's
  `Unique -> Sort` instead of HashAggregate, alongside known
  row-order/position shuffles.

Evidence: `analysis/m0146/m0146-0005/slice61/`.

Still ledgered (slice 60): why PG keeps the per-worker Unique on SF0.25
Q38/Q87's catalog\_sales and web\_sales branches. Under PG's Unique formula,
the split arm costs the per-worker comparisons more than the unsplit one,
and PG adds the unsplit arm first. So PG's choice points at an input-path
difference, not at pricing.

## Slice 62: M0146-0005bj — Aggregate and SetOp nodes carry their path's cost

Slices 58/59 ledgered it: `Aggregate` and `SetOp` embedded no `PlanCost`,
so every reader priced them with `DeriveLegacyDisplayCost`. That is a
monotone display estimate: max child startup, sum of child totals. The
readers are EXPLAIN and every planner site that prices an input node from
its cost (`legacyDisplayCostOf`): subquery leaves in the join search,
upper-rel seeds, and set-operation arms. A path-chosen partial
HashAggregate printed 2845.67 where its path cost 1949.84. Q66's Parallel
Append printed 11759..18259 (the sum) where its path cost 7496..12758.

- `Aggregate` and `SetOp` embed `PlanCost`; the `createPlanNode` funnel
  stamps the chosen path's cost and rows on them. Nodes the legacy
  rewriter builds stay unstamped and keep the legacy derivation.

Movement: printed costs now follow the paths. Q87's `SetOp Except`
prints 42288.96..43153.81 (PG 42449.87..43284.81), and Q8's `HashSetOp
Intersect` prints 9265.66..9266.16 (PG 9268.15..9268.65; legacy
7029..9502).

The sharper costs cost one match at each scale: TPC-DS PLAN-PARITY SF0.25
27 → 26 and SF1 24 → 23, both Q8. Q8's `store ⋈ INTERSECT` leaf is priced
from the INTERSECT node. With its correct startup the join search now
builds `Hash Join (Gather(... ⋈ store), HashSetOp)` at 28307 over PG's
`Nested Loop (Gather, Materialize(store ⋈ Materialize(HashSetOp)))`,
which goopg prices about 28480. The two shapes are within about 0.6% in
goopg's model. The legacy estimate's lower startup had tipped the
election PG's way by accident. Filed as M0146-0005bk.

All TPC-DS queries execute at both scales (the fire set fired on every
query, since every cost text moved), and the sweep's plan-shape channel
is unchanged. TPC-H shapes are identical. The regress runner (7 cases)
shows only the known join.sql row-order flap. Evidence:
`analysis/m0146/m0146-0005/slice62/`.

## Slice 63: M0146-0005bk — nested loops are offered before the hash join

Slice 62 cost TPC-DS Q8 its match. The fix is PG's add\_path tie rule
combined with its arm order. With `enable_nestloop = off`, PG 18.3's own
hash alternative for Q8 costs 28305.93 against the 28512.75 nested loop it
keeps. The two paths are within STD\_FUZZ\_FACTOR on both startup and total,
and pathkeys, rows and parallel safety are equal. So add\_path "arbitrarily
keep[s] only the old path", and the old path is whichever was offered
first. `add_paths_to_joinrel` offers `match_unsorted_outer`'s nested loops
(joinpath.c:290, then `consider_parallel_nestloop`) before
`hash_inner_and_outer` (:212 runs after them). goopg filed its hash arm
first. Its comment said the order could only change an exact tie; under
fuzzy ties it decides.

- `addPathsForJointype` now files the serial nested loop, the NLI
  (parameterised) arm and the partial nested loop before the serial and
  partial hash arms. The merge arms keep their place ahead of both.

Test: `TestJoinArmsOfferNestLoopBeforeHash` captures the path trace for one
join pair and requires the first nestloop offer to precede the first hash
offer. Base offered `mergejoin, join.hash, join.nestloop, ...`, so the test
fails there.

Movement:

- TPC-DS PLAN-PARITY match SF0.25 26 → 27 and SF1 23 → 24 (Q8 = PG again).
- SF0.25: join-order 55 → 54, join-method 30 → 29, scan-type 38 → 37,
  parallelism 36 → 35.
- SF1: join-order 61 → 60, scan-type 43 → 41, qual-placement 9 → 8, and
  aggregation-strategy 22 → 23 (Q26 moves between categories).
- The fire set (Q8/Q16/Q19/Q26/Q40/Q64/Q72) executes at both scales.
- TPC-H shapes are identical.
- In the regress runner, join.sql's "non-unique rel is not chosen as
  inner" case now plans PG's `Hash Join` with j3 outer, where it had a
  nested loop.

Evidence: `analysis/m0146/m0146-0005/slice63/`.
