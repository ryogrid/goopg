# M0146-0005 (part 2): residual triage 2026-09-26 and slices 15+

Continuation of [m0146-0005-join-order-burndown.md](m0146-0005-join-order-burndown.md)
(slices 1-14) — split per the design-doc size rule (D3). Same task,
same census family; see part 1 for the family scope and the trace
methodology (DPPATH vs PLANCAND).

## Residual triage (2026-09-26)

After slices 1–14 the remaining join-method/join-order records mostly have
causes outside join search:
- stale unpadded `char(n)` TPC-DS data plus the probe multiplier (Q79, Q55,
  Q23, Q30);
- sublink decorrelation (Q1, Q92);
- aggregation strategy (Q65);
- a PG cost tie (Q4, Q11);
- set-op strategy (Q38, Q87);
- CTE/upper structure (Q2, Q31, Q97).

Q8 and Q14 are untraced. Evidence:
`analysis/m0146/m0146-0005/residual-triage-20260926/`.

## Slice 15 (M0146-0005o): INTERSECT / EXCEPT row estimates

`estimateSetOp` now follows `generate_nonunion_paths`: each arm's group
count is its rows when grouped, distinct or itself a set operation, else
`estimate_num_groups` over its outputs. INTERSECT takes the smaller count
and EXCEPT the left; the ALL forms use rows. goopg had halved the input.
TPC-DS SF0.25 Q8 now plans PG's nested loops. SF1 Q8 diverges only because
PG's SF1 `store` table has no statistics. Evidence:
`analysis/m0146/m0146-0005/slice15/`.

## Slice 16 (M0146-0005p): UNION keeps its whole input

`generate_union_paths` takes a non-ALL UNION's group count as the whole
input (the worst case), and `estimateSetOp` now does the same instead of
halving. No TPC-DS or TPC-H plan changes. Evidence:
`analysis/m0146/m0146-0005/slice16/`.

## Slice 17 (M0146-0005q): SETOP_SORTED for INTERSECT / EXCEPT

goopg had only the hashed set-op executor, while PG 18 also offers
SETOP_SORTED. That path has the same total as the hashed one and a
startup of just the inputs' startups, so it wins whenever both inputs are
presorted. Q38/Q87's DISTINCT arms are presorted. The change adds:
- a merge executor (`nextSorted`, nodeSetOp.c semantics for all four
  commands);
- the planner candidate over presorted arms (a Unique over an all-column
  Sort, or a nested sorted set-op), with PG's cost and pathkeys;
- the `SetOp <cmd>` EXPLAIN label.

Q38 now diverges at depth 3 (was 2) and Q87 at depth 4 (was 1). PG's
INTERSECT smaller-input swap is the next Q38 residue. Evidence:
`analysis/m0146/m0146-0005/slice17/`.

## Slice 18 (M0146-0005s): range estimates take PG's eq_selec

goopg scored `x >= c` like `x > c` and `x < c` like `x <= c`. PG's
ineq_histogram_selectivity estimates `x <= c` from the histogram,
rescales the first bin, and subtracts eq_selec = 1/(ndistinct - #MCV) for
`<` and `>=`. Without that step `d_year BETWEEN 1999 AND 2001` lost one
eq_selec: 705 rows against PG's 1049 (actual 1096). The low estimate
flipped Q14's INTERSECT swap (0005r, not landed) the wrong way.
`histogramOpSelectivity` now follows PG; the estimate is 1069 rows.

The better estimate moved Q38/Q87's first DISTINCT arm across add_path's
1% fuzz, from Sort + Unique to HashAggregate, and both queries lost
slice 17's sorted SetOp. PG keeps the Unique path on the arm rel for its
pathkeys and uses it for the sorted SetOp. `sortedSetOpArm` rebuilds it
for a hashed DISTINCT arm. The SF0.25 census is identical to HEAD. At SF1
Q38 (depth 2 to 3) and Q87 (depth 1 to 4) now plan PG's sorted SetOp.
Evidence: `analysis/m0146/m0146-0005/slice18/`.

## Slice 19 (M0146-0005r): INTERSECT smaller-input swap

PG puts the INTERSECT input with fewer groups on the left. goopg now does
the same in `swapIntersectInputs` using `setOpArmGroups`. The swapped
node keeps the written first arm's output schema (`SetOp.pinnedSchema`).
The first attempt had flipped Q14's arms where PG did not; that came from
the missing eq_selec, which slice 18 fixed. Q38 now diverges at depth 4
(was 3) at both scales, on PG's partial Unique + Gather Merge; nothing
else changes. Evidence: `analysis/m0146/m0146-0005/slice19/`.

## Slice 20 (M0146-0005t): EXPLAIN names a column by its own level

Q14's depth-7 SF0.25 record was a rendering difference, not a plan one.
goopg printed the cross\_items join as `store_sales.ss_sold_date_sk =
date_dim.d_date_sk` where PG prints `d1.d_date_sk`. Two causes:
- `createSeqScanPlan` dropped the leaf's `RTID`, so search-built seq scans
  had no range-table name;
- the planner restarts SourceTableIdx in every query level, so the
  statement-wide lookup handed a CTE body's columns the outer query's
  relations.

`createSeqScanPlan` now carries `RTID`. `explainNames.columnIn` resolves a
column against the rendered node's subtree, then its ancestors, stopping
at CTE and set-operation boundaries (PG's set\_deparse\_plan namespace).
Scan labels print `<relation> <refname>` as ExplainTargetRel does. Q14's
record moves to `CTE avg_sales`. The rendering category drops by 2 at
each scale. Evidence: `analysis/m0146/m0146-0005/slice20/`.

## Slice 21 (M0146-0005u): grouping inputs read the searched rel's
cheapest-total path

TPC-DS Q22's SF0.25 `join-method` record was not a candidate-pool gap —
the parallel hash join for `inventory ⋈ date_dim` was offered, accepted
and strictly cheaper than the winner (DPPATH totals 27443 vs 96692). The
seam commits the searched subtree to `finalPath`, the
`get_cheapest_fractional_path` pick, which under `LIMIT 100` is a
startup-optimal serial NLI chain. PG's grouping stage never sees that
pick: `add_paths_to_grouping_rel` reads
`input_rel->cheapest_total_path` (planner.c:7122), so the upper arms are
priced over the costed Gather path. goopg's `createGroupingPaths` seeded
every arm from the committed node tree, so the gathered arm
re-parallelised the serial subtree while the rel's cheapest-total path
never stood in the comparison.

`searchedCheapestTotalInput` (searchedtree.go) is the equivalent read at
the node boundary: when the searched input's stamped rel carries a
strictly cheaper `CheapestTotal`, it rebuilds a node over that path
through `searchedBoundaryRebuild` — a coverage pre-check against the
boundary window, then `createPlanAtSearchRootRange` itself, then a
positional schema compare against the committed root — and splices it
under the same `*Project`/`*Sort` wrappers. `createGroupingPaths` swaps
`seed`/`child` to that input for `addGroupingPaths` only; the
partial-agg split keeps the committed child because `parallelSeedCost`
denominates serial-subtree currency. Every unprovable case — no
searched root, no cheaper total, an unreproducible boundary row, a
lowering panic — declines and the committed input stands.

Q22 plans `Gather -> Nested Loop -> Parallel Hash Join` now and reads
as a census MATCH (PG's extra `Parallel Hash` build wrapper is
normalised); values identical to PG 18.3 SF0.25, 102 rows. Goopg-vs-
goopg self-diffs on the same private clones: Q22 is the only shape
change across all 99 TPC-DS queries, and all 22 TPC-H queries are
unchanged. Evidence: `analysis/m0146/m0146-0005/slice21/`.

## Slice 22 (recon): Q82 = btree skip-scan; Q8 decomposes — no code

TPC-DS Q82's `join-method` depth=7 record: PG drives `NL (item ->
inventory_pkey probe)` where the probe's `Index Cond: (inv_item_sk =
item.i_item_sk)` binds the SECOND column of
`btree (inv_date_sk, inv_item_sk, inv_warehouse_sk)` — PG 18's skip
arrays (`_bt_skiparray`, nbtpreprocesskeys.c/nbtutils.c) cycle the 209
distinct `inv_date_sk` values at one descent each, costed by
`btcostestimate`'s `num_sa_scans *= get_variable_numdistinct`
(selfuncs.c:7342+). goopg admits index keys only on a gapless leading
prefix, so no `{0}`-parameterised path exists on `{1}` and the joinrel
elects `Parallel Hash Join`. The gap was ledgered under M0145-0029-2b
with no consumer; Q82 is the first measured one. Filed `M0146-0005v`.

TPC-DS Q8's `join-order` depth=3 leaf-set record decomposes into: (a) a
missing `Subquery Scan on a1` leaf — PG plans each set-op leaf arm via
`subquery_planner`, goopg plans arms inline — filed `M0146-0005w`;
(b) two missing `Materialize` wrappers (M0146-0010); (c) an INTERSECT
arm-order estimate (`ca_zip` n_distinct 3124 vs goopg's substr() arm
group estimate ≤ 1070 — M0146-0009 territory). Q14's depth-7 record is
gone from the post-21 census.

Evidence: `analysis/m0146/m0146-0005/slice22/`.

## Slice 23: M0146-0005v — btree skip scan landed (parameterized probes)

PG18's `_bt_skiparray` mechanism (nbtpreprocesskeys.c): a btree index
key on a non-leading column files an equality scan over procedurally
generated skipped-prefix values — one bounded descent per distinct
leading value. Landed for PARAMETERIZED probes only.

- Plan contract: `Path.IndexSkipPrefix` / `IndexScan.SkipPrefix` —
  `Keys[i]` binds `Index.Columns[SkipPrefix+i]`; `Key`/SAOP/range are
  invalid in combination (executor reports, never interprets).
- Admission (`pathparamindex.go`): btree, ≥2 cols, contiguous equality
  run past column 0, ordinary ASC skipped columns, NOT NULL unbound
  columns (or null-keyed index). Emitted ALONGSIDE the prefix
  candidate — PG files all usable index paths and costs them.
- Costing: `num_sa_scans = ∏ ndistinct(+1)` per skipped column with
  PG's two reverts (default ndistinct, product > index->pages) — a
  revert drops the run's quals from the bound set, so `boundSelectivity`
  splits from heap-side `selectivity`. `numIndexTuples =
  rint(numIndexTuples/num_sa_scans)` shared with the SAOP arm.
- Executor: lazy cursor-driven enumeration per rescan (no
  materialized skip list → early-stop preserved), tuple-format datum
  decode/re-encode + blob-format byte-increment prefix successors.
- Sibling audits: `unnest.go` harvest gained the offset;
  `lateralProbeIsPartialProbe` MUST admit skip probes — its refusal
  produced a path/node twin mismatch that panicked
  `gatherChildPlan` (Q16/Q72) since `partialPathDrivingKind` admits
  parameterized `PathIndexScan` inners.
- Measured: Q82 elects `NL -> Index Scan inventory_pkey
  (inv_item_sk = ...)`; fire-set census `jointree-search` 23→21,
  `join-method` 45→43, `join-order` 73→72; five moved plans
  {Q16,Q37,Q72,Q82,Q94} all skip-scan elections matching PG shapes.
- Executor-capability bounds (the SF1 fire-set lesson): a fat skip
  probe is a bulk scan in disguise — `maxSkipProbeRows=600` on
  per-execution rows, `maxSkipProbeLifetimeRows=5e8` on
  rows×loopCount. Witness: Q72 SF1 elected `NL(cs→inv-skip)` by an
  ~8% margin the join-cost model drifted on, then ran ~9.5k rescans
  of a ~980ms/rescan probe (>600s timeout). PG files the same path
  and its model rejects it — the residual is ~8k of join-cost drift
  in the surrounding subtree, `join-order`/`costing` family. The
  bound declines the 709-row probe under every outer relset; the
  search re-elects PG's d2-first order (plan = baseline, ~5s), and
  SF1 Q72 left the fire set. SF0.25's 527-row probe stays admitted
  (PG-matching election preserved, 168s — parity shape inside the
  window). Final fire-set: `introduced=none` at both scales.
- Deferred: unparameterized skip (restriction/bitmap/IOS), DESC/expr
  skipped columns, null-keyed blob e2e.

Evidence: `analysis/m0146/m0146-0005/slice23/`.

## Remaining records

Post-slice-21 census, still to be worked:
- TPC-H: Q17 (`join-method`; waits on M0146-0012's correlated-sublink
  JOIN clauses) and Q19 (aggregation first divergence); Q9's first
  divergence is `qual-placement`/`sort-strategy` at depth 0.
- TPC-DS SF0.25: `join-method` Q1, Q23, Q30, Q55, Q65, Q79, Q92
  and `join-order` Q4, Q8, Q11 — attributed per the slice-22 table:
  owner-blocked (Q23/Q30/Q55/Q79), filed children (Q8 →
  0005w), other families (Q1/Q92 sublinks, Q65 aggregation), or cost
  ties (Q4/Q11). Q82's record closed at slice 23 (skip-scan landed;
  it moved to the D3-partialpath class along with Q37 — remaining
  diffs are the parallel/IOS substrate, not join shape).

Re-run the first-divergence census on each slice's capture before choosing
the next mechanism.

## Slice 26 (recon): Q19 `{ss,dd,item}` seed order = priced inputs, routed

Post-0027-slice-3, Q19's SF0.25 record is a depth-10 join-order divergence:
PG seeds `store_sales ⋈ date_dim`, goopg seeds `store_sales ⋈ item`.
Instrumented-PG candidate trace + goopg DPPATH show **both** engines
generate and price both seeds and both `{ss,dd,item}` NL orientations,
and `addToPartialPathlist` already carries `add_partial_path`'s
fuzzy/incumbent semantics. PG's two arms land 6.3 units apart — a fuzzy
tie resolved by filing order; goopg's land 80.25 apart, so `ss ⋈ item`
wins outright. The whole flip decomposes into priced inputs:

- −44 / −21 on the two partial-hash builds from physical `relpages`
  (item 1242 vs 1284, dd 1405 vs 1424 — the post-R23 heap-density
  residual on varchar/char-rich dims; closes the scan-cost arithmetic
  exactly), and
- −66.5 on the memoized `date_dim` probe (per-probe 0.287 vs 0.324, ~11%,
  inside the `indexProbeMultiplier=2` compensation).

Routing: density residual → `m0140-0005-nonplanner-heap-density-floor`
ledger row; probe epsilon → M0142-0005c lineage. No planner change is
justified — the candidates exist, are priced, and are adjudicated with
upstream-identical semantics; the record converges when the inputs do.

Evidence: `analysis/m0146/m0146-0005/slice26/`; fix-plan entry
M0146-0005z.

## Slice 27: parameterized bitmap-probe partial NLI is gatherable

Q55: PG elects `Gather -> NL(-> NL(Parallel Seq Scan item, Bitmap Heap
Scan store_sales probe), Memoize -> Index Scan date_dim)`; goopg ran a
serial `Nested Loop` over `Gather(Parallel Hash Join ss ⋈ dd)` instead.
The DP trace showed the PG-shaped partial NL already sat at the head of
the `{0,1,2}` partial pathlist at 16255.65 — cheaper than every serial
candidate — yet `cpgather` admitted the survivor and `makeGatherPath`
still filed no `Gather`: `partialPathDrivingKind`'s PathNestLoop probe
arm admitted only `PathIndexScan` (or `PathMemoize`-wrapped index)
inners, so the parameterized `PathBitmapHeapScan` inner classified the
subtree `PathPrebuilt`.

Two surfaces move together, fail-closed:

- `partialPathDrivingKind` (`gatherpaths.go`): the probe arm now admits
  `PathBitmapHeapScan` iff it has exactly one child and that child is a
  `PathBitmapIndexScan` (the `IndexClauses` requirement still applies);
  a `PathMemoize`-wrapped *bitmap* probe stays refused because
  `createPlan` unwraps memoize into `createNestLoopBitmapJoinPlan` and
  would lose the priced cache.
- `NestedLoopIndexJoinIsPartialCapable` (`parallel.go`): gains
  `nliBitmapProbeIsPartialProbe` — `*BitmapHeapScan` whose `Outer` is
  exactly one `*BitmapIndexScan` carrying probe keys. Separate from
  `lateralProbeIsPartialProbe` on purpose: the decomposed-lateral
  executor only supports index/index-only probes, while the fused NLI
  executor already drives a `BitmapHeapScan` inner through `nliInner`
  (`BindOuter` + `Rescan` rebuild a private TID bitmap per outer row,
  `pbm == nil`).

No executor code changed — every claim walk (`attachAll`,
`collectBitmapScans`, `drivingScan`) reads the same exported predicate,
so widening it keeps planner and executor in lockstep: claims attach to
the NLI outer only, the inner bitmap is never collected into
`prebuildBitmap`'s shared claim set, and each worker rescans its own
serial bitmap per outer row (no N-copy over-counting — pinned by a
1/2/4-worker identity test and a `collectBitmapScans` inner-isolation
case).

Result: Q55 elects `Gather Merge -> Partial GroupAggregate -> Sort ->
NL(NL(Parallel Seq Scan item, Bitmap Heap Scan ss), Memoize(dd))` —
shape-identical to PG modulo the deliberate partial-agg split
(M0146-0027 slice 3); rows=68, checksum `fe343d36717a4fb5` = oracle.
Sweep collateral: Q3/Q37/Q75/Q76 take the same gather over bitmap-probe
NLs; all checksums clean.

Evidence: `analysis/m0146/m0146-0005/slice27/`; fix-plan entry
M0146-0005aa.
