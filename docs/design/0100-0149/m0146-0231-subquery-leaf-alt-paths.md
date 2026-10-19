# M0146-0231 — subquery leaves offer the subquery's surviving paths; join paths are prechecked

Status: slice 1 landed 2026-10-19. Parent: M0146-0214. Evidence: `analysis/m0146/m0146-0231/`.

## 1. Problem

TPC-DS SF0.25 Q64 diverged from PG at depth 5 (join-order). M0146-0214 slice 5 traced it to GEQO tour 14 join 6.
There, PG files a partial Merge Join over `(b 1 7 9 10 11 12 16) ⋈ cs_ui`, and its Gather Merge
(1003.38..6.42 × 10⁹) is the joinrel's cheapest path. The partial merge's inner is a parallel-safe SubqueryScan
path of `cs_ui`, ordered on `cs_item_sk`: 0.7225..42006.29. The path is a GroupAggregate over
`catalog_returns_pkey` ⋈ Memoize(`catalog_sales_pkey`), confirmed on `:65438` with `enable_seqscan=off`.

goopg's derived leaf offered the join search a single prebuilt path: the subquery's cheapest plan. That plan holds a
Gather Merge, so it is not parallel-safe, and no partial merge could be built.

## 2. What PG does

- **`set_subquery_pathlist` (allpaths.c).** Every path in the subquery's final rel becomes a SubqueryScan path.
  Its pathkeys come from `convert_subquery_pathkeys`. Its parallel_safe is
  `rel->consider_parallel && subpath->parallel_safe`.
- **`add_path`.** It keeps a parallel-safe path beside a cheaper unsafe one with the same pathkeys. So the
  subquery's GROUP\_AGG rel keeps both the GroupAggregate over the Gather Merge and the parallel-safe
  GroupAggregate.
- **`set_rel_consider_parallel`, RTE\_SUBQUERY arm.** It reads only `limit_needed`, the rel's quals and its
  target. It never reads the subquery's paths. An inlined CTE is an RTE\_SUBQUERY (`inline_cte`).
- **`add_path_precheck` and `add_partial_path_precheck` (pathnode.c).** `try_nestloop_path`,
  `try_mergejoin_path` and `try_hashjoin_path` (joinpath.c) call them, and so do the partial twins. The check
  uses the initial cost, before the path is built:
  - A serial path is rejected if an existing path of the same parameterization dominates it on pathkeys and on
    both costs. Startup counts only when the rel considers it. **Parallel safety is not consulted**, so a costlier
    parallel-safe join never reaches add\_path's parallel\_safe rule.
  - A partial path is decided outright against comparable partial paths. Otherwise it must also pass
    `add_path_precheck` against the complete paths, with its total standing in for its startup: "If it loses even
    before accounting for the cost of the Gather node, we should definitely reject it."

## 3. What goopg did, and the change

1. **Grouping paths carry parallel\_safe** (`groupingpaths.go`).
   - The GROUP\_AGG rel gets make\_grouping\_rel's `consider_parallel` (`groupedRelConsiderParallel`). That
     requires the input rel's flag, a parallel-safe target and parallel-safe HAVING. HAVING is now passed to
     `createGroupingPaths`.
   - Every candidate is filed through `addAggPath`, which applies create\_agg\_path's
     `rel->consider_parallel && subpath->parallel_safe`.
   - The input seeds carry their own safety. A searched candidate carries the searched path's flag. A finished
     node is unsafe when it holds a Gather (`inputPathParallelSafe`).
2. **The grouped rel is recorded per statement.** It goes in `planState.groupedRels`, keyed by the Aggregate the
   winner was written onto, so no package global is involved (P0-E8).
3. **Derived and inlined-CTE leaves offer the alternatives** (`addSubqueryAltPaths`, ctescanpathkeys.go).
   - It runs after `setBaseRelConsiderParallel`, because PG's set\_rel\_size precedes set\_subquery\_pathlist.
   - Each surviving PathAgg other than the winner becomes another prebuilt path on the leaf rel. The leaf is
     rebuilt as the finished Aggregate (after its scope's post-passes) with the alternative's input and strategy,
     under clones of the pass-through wrappers between it and the leaf: Filter, Project, SubqueryScan and the
     inlined CTEScan. Stamped costs are cleared on the clones; the Aggregate carries its own path's stamp.
   - Pathkeys: an alternative ordered as the winner is gets the winner's converted ordering, because
     convert\_subquery\_pathkeys is a function of the path's pathkeys.
   - Fail-closed: an alternative whose spec is not the winner's, or whose input publishes a different row, is not
     offered. The index-narrowed variant is an example of a spec that differs.
4. **Gather-holding leaf paths are parallel-unsafe** (`planHasGatherNode`, setBaseRelConsiderParallel).
   - An inlined CTE leaf now takes the RTE\_SUBQUERY arm of `relConsiderParallel`.
   - The planned-subtree hazard walk passes through Gather Merge, Incremental Sort and Memoize. That walk is
     goopg's stand-in for "quals and target are safe". A Gather inside makes the path unsafe, not the rel.
5. **The prechecks are ported** (`addPathPrecheck` and `addPartialPathPrecheck`, path.go). They are called from
   all five serial producers (hash, param hash, merge, nested loop and NLI) and all four partial producers.
   - Each call uses the initial cost: `hashJoinInitialCost` (the build, the probe's hashing and the batch I/O,
     now shared with `hashJoinCost` through `hashJoinBatchCost`), `mergeJoinInitialCost` and
     `nliNestLoopInitialCost`.
   - PG stops scanning the pathlist at the first path it cannot be dominated by, relying on the list's order.
     The port skips such paths instead, which gives the same answer on a sorted list.

Without (5), (1)–(4) made several SF0.25 tours *cheaper* than PG by about 68%. Two partial-path cases caused it.
Partial hash joins whose inner is cs\_ui's 42006 path were filed although a complete path at 13873 dominates them.
Serial merge joins over that path survived add\_path only on parallel safety. PG rejects both in the precheck.

## 4. Result

| instrument | HEAD 9e62d3cab | slice 1 |
|---|---|---|
| SF0.25 Q64 GEQO tours equal to PG's, in order | 252 | 326 |
| SF0.25 Q64 evals before the first differing tour with worth off > 1e-2 | 15 | 0 |
| SF0.25 Q64 plan | depth-5 join-order | **MATCH** |
| SF1 Q64 GEQO tours equal to PG's, in order | 258 | 262 |
| SF1 Q64 plan | MATCH | depth-4 join-order (see §5) |
| TPC-DS fire-set PLAN-PARITY match, SF0.25 / SF1 | 97 / 95 | 98 / 94 |

On SF0.25, tour 14's join 6 is now PG's Gather Merge over the partial Merge Join, cost for cost. The ~7.6 total
difference is inherited from the pre-existing 0.31 residual at join 3, a relative 1.2 × 10⁻⁹.

## 5. Residuals

- **SF1 Q64.** Both builds reach PG's best tour (eval 114, worth 49259.03875 against PG's 49259.03849).
  - The tours diverge from eval 262; HEAD diverged from 258. After that point the candidate evaluates a second
    tour with the same worth (eval 363), and the GEQO pool's tie order picks that one. PG never reaches that
    tour.
  - HEAD matched only because its post-258 tours found no tie.
  - Before 262 the candidate is closer to PG than HEAD on 17 evals and further on 1 (eval 160, 2.6 × 10⁻⁴).
  - The fix is the remaining sub-1% worth residuals, which are M0146-0214's.
- **Partial paths of the subquery.** `set_subquery_pathlist` also files the final rel's partial paths. This slice
  offers complete paths only.
- **Ungrouped subqueries.** Joins only, or ORDER BY / DISTINCT / window / LIMIT above the grouping, still offer
  one path.
- **Precheck order.** The precheck reads the pathlist as goopg's producer order has filled it. PG's
  add\_paths\_to\_joinrel order is sort\_inner\_and\_outer, match\_unsorted\_outer, then hash\_inner\_and\_outer.
  Where goopg's order differs, a parallel-safe loser can still survive.

## 6. Gates

- units, TestPort\_RegressSuite and TestPort\_Isolation\* pass.
- tpch-spotcheck passes. The acceptance arm reports values identical to baseline.
- tpcds-sf025 sweep passes on all 99 queries. Only Q64's plan changed; its runtime went from 21 s to 32 s on PG's
  plan, with the same checksum.
- tpcds-fireset passes. The fired Q64 and Q77 results are byte-identical between baseline and candidate.
- Pins: `m0146_0231_test.go`. The precheck and agg-safety pins were mutation-checked.
