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
## Slice 28 (recon): Q17/Q25/Q29 gather height = probe-cost epsilon, routed

Post-slice-27, the last `parallelism`-classified census family is Q17,
Q25, Q29: both engines gather the same `Gather Merge -> Sort -> NL
chain` shape, but goopg pulls `catalog_sales` **inside** the partial
subtree (the Gather Merge covers six relations) while PG gathers the
five-relation chain and probes `catalog_sales` above it via index.

Instrumented-PG plancand + goopg DPPATH on private SF0.25 clones show
the candidate sets are **complete on both sides**: goopg files the
PG-shaped serial arm (`nestloop.index` over the 5-rel outer probing cs,
4862.38) and loses it to the six-rel `gather.merge.sort` (4841.90,
Δ≈20.5 ≈ 0.4%); PG files both gather arms at the six-rel rel and
rejects them `via=tie` against the already-filed `NL(GM5, cs)` (all at
4715.94–4715.98 — a dead fuzzy tie kept by filing order).

The sign flip originates at the 3-rel probe arm sharing the `{sr,d2}`
partial outer: PG prices `ss` pkey probing cheapest (NL arm 3714.44 vs
cs arm 3736.47), goopg prices `cs` `item_sk`-index probing cheapest
(3839.84 vs ss 3860.95). Per-probe param costs bound to `sr`: goopg
ss=2.055 / cs=1.916; PG ss=1.313 / cs=1.458 — a ~0.3-unit ordering flip
amplified ~144× by inner repetition. Same probe-cost-epsilon class as
slice 26, including the uniform ~0.5-0.75 uplift inside the
`indexProbeMultiplier=2` compensation.

Routing: probe-cost epsilon → M0142-0005c cost-model lineage. The
census signature stays `parallelism` mechanically, but the true cause
is documented; re-file as cost-adjudication when it next re-runs.
No planner change is justified.

Evidence: `analysis/m0146/m0146-0005/slice28/`; fix-plan entry
M0146-0005ab.

## Slice 29: M0146-0005ac — Parallel Append arm order + residual routing at `bb431e90c`

Census on the post-M0146-0010 SF0.25 capture (83 divergent / 16 match):
Q37, Q59 and Q71 moved to new, unrouted `join-order`/`join-method` records.

- **Q71 (fixed here).** PG's `create_append_path` (pathnode.c:1343-1361)
  sorts a parallel-aware Append's subpaths: non-partial first by total cost
  descending, partial by startup descending then total descending, relids
  breaking ties. goopg kept the written order. goopg's union is a left-deep
  chain of two-child `*SetOp` links, so the sort runs at plan construction
  (`orderParallelAppendArms`, `parallelappendorder.go`, called from
  `createSetOpPlan`): each link collects the flattened arms with the paths
  that built them, sorts, and rebuilds the chain; every rebuilt link pins the
  chain's original output schema (the written first arm's names), and each
  arm keeps its claimed-whole mark. An inner link, already reordered when it
  was built, hands its ordered arms up in `SetOp.appendArms` — re-pairing by
  position after an inner reorder produced store, web, catalog on the first
  live run. Q71 now lists store, catalog, web as PG does; its first
  divergence is PG's `Subquery Scan on "*SELECT* n"` wrapper over each arm
  (the Subquery-Scan family, M0146-0026).
- **Q37 (routed).** The join order is decided by the `inventory_pkey`
  skip-scan probe (`inv_item_sk` is its second column): goopg 3512 per loop
  vs PG 1824 — exactly the `indexProbeCostMultiplier = 2` calibration. With
  `GOOPG_INDEX_PROBE_MULT=1` goopg elects PG's order (item → inventory
  1800.93 vs PG 1824.06, then date\_dim, then catalog\_sales). Routed to the
  M0142-0005c cost-model lineage with 0005z/0005ab; the remaining inner
  `Index Scan` vs PG `Index Only Scan` is M0146-0019.
- **Q59 (not traced).** PG nests `Materialize(Hash Join(wss⋈store, wss⋈store⋈d))`
  under a nested loop driven by `date_dim d_1`; goopg merge-joins the two
  halves. Unchanged by the multiplier.
- Found on the way (ledgered): goopg files no skip scan for a
  constant-bound probe (`inv_item_sk = 5` seq-scans; PG skip-scans at 1817).

Movement: SF0.25 `parallelism` 48 → 47, `qual-placement` 19 → 18; SF1
`qual-placement` 20 → 19; match unchanged (16 / 14). Sweep 96/96 (5 plans
changed: Q5 Q66 Q71 Q75 Q76), fire set 5 fires, no introduced timeouts.

## Slice 30 (recon): M0146-0005ad — Q59 has no FROM-subquery pull-up, routed

Evidence `analysis/m0146/m0146-0005/slice30/`. With the DP trace on, goopg
solves Q59 as three separate problems — `{wss,store,d}` for each of the
subqueries y and x, then `{y,x}` — so PG's order, which joins x's
`wss_1 ⋈ store_1` to all of y and adds `date_dim d_1` last through a nested
loop over a Materialize, is outside goopg's search space. The cause is
architectural: `planSubqueryRangeVar` plans every FROM-clause subquery as its
own scope (M0146-0005w only removes the `Subquery Scan` label for simple
bodies), while PG's `pull_up_simple_subquery` (prepjointree.c) splices a
simple body's FROM items into the parent jointree and substitutes the
subquery's output columns by expression (`pullup_replace_vars`). The
M0145-0001 contract (§4.3, "the same mechanism covers FROM-clause derived
tables") planned this and it was never built.

Reach on TPC-DS: simple multi-relation FROM subqueries appear in Q2, Q51,
Q59 and Q93; only Q2 and Q59 have a sibling FROM item, so only they can
change join order (Q2 currently diverges earlier). TPC-H Q7/Q8/Q9 are
unaffected — their lone FROM subquery already holds every relation. Filed as
M0146-0028. Movement: none — routing recon.

## Slice 31: M0146-0005ae — expression group keys carry their emission order

After M0146-0028b pulled up TPC-H Q7/Q8's lone FROM subqueries, both queries'
first divergence was the very top: PG ends in the `GroupAggregate` (its
output is already in `ORDER BY` order), goopg added a `Sort` on the same
keys. Pre-existing and not pull-up specific: `SELECT a % 3, count(*) … GROUP
BY 1 ORDER BY 1` on a sorted aggregate also re-sorted. Both emission-order
twins — `aggregateEmissionPathkeys` (finished `*Aggregate`,
upperorderedinput.go) and `groupingEmissionPathkeys` (`PathAgg`,
upperorderedgrouping.go) — accepted bare-column group keys only ("only a
column has a name"). The claim they publish names the OUTPUT POSITION of the
key, and the ORDERED step matches it with `exprEqual`, whose ColumnRef
identity is `Index` alone (names excluded, exprwalk.go). So an expression key
whose child sort key is the same expression is as sound as a column key.
Both twins now admit it together; the output-name check stays for column
keys. PG needs no such argument: its pathkey is the key's EquivalenceClass.

Movement: TPC-H PLAN-PARITY match 7 → 8 (Q7 = PG); Q8's first divergence
advances from depth 0 to depth 9 (`lineitem_part_supp_fkidx` Index Scan vs
goopg's Bitmap Heap Scan). TPC-H `join-order` 12 → 11, `join-method` 6 → 4,
`sort-strategy` 8 → 6, `parallelism` 9 → 7. TPC-DS unchanged.

## Slice 32: M0146-0005af — the partially grouped rel's own add\_partial\_path

A fresh TPC-H census at `7c72251a2` showed its largest first-divergence
family at the partial aggregate. Q4, Q5 and Q12 each show PG's
`Partial GroupAggregate` directly under `Gather Merge`, where goopg has
`Sort -> Partial HashAggregate`. PG costs the hashed partial lower (Q4:
68911.18 against 69117.54) and still elects the sorted one. The serial plan
behaves the same way: 192198 hashed against 193158 sorted, and `add_path`
keeps the sorted one.

- **The election PG runs.** `create_partial_grouping_paths` files the sorted
  partial (`Partial GroupAggregate -> Sort`) and then the hashed partial on
  `partially_grouped_rel` through `add_partial_path` (pathnode.c:795). The
  two totals are within `STD_FUZZ_FACTOR`, so the sorted partial wins on its
  pathkeys and evicts the hashed one. `gather_grouping_paths` then wraps
  only what survived. Neither `Gather -> Partial HashAggregate` nor the
  presorted `Gather Merge -> Sort -> Partial HashAggregate` ever exists.
- **What goopg did.** `addPartialAggSplitPath` filed the finished arms on
  the grouped rel and let that rel's `add_path` choose. There, the presorted
  split undercut the sorted-input split. The two arms share a transport, a
  merge and a finalize, so a 0.3% saving at the partial became the whole
  margin.
- **Change.** `hashedPartialAggSurvives` replays `addToPartialPathlist` over
  the two partial paths in upstream's filing order: sorted first (the
  can\_sort block), hashed second. It applies the same fuzz, pathkeys and
  `disabled_nodes` rules as every other partial rival. `enable_sort=off`
  disables the sorted partial, so the hashed partial survives, as upstream.
  An evicted hashed partial files neither the presorted split nor a hashed
  split. The sorted-input arm's partial path is now built once, by
  `sortedInputPartialAgg`, and inherits its Sort's disabled count.
- **Not filed.** PG's `Finalize HashAggregate -> Gather -> Partial
  GroupAggregate` still exists upstream over the surviving sorted partial
  (its hashed final reads `cheapest_total_path`). goopg's split arm uses one
  strategy for both halves and cannot express it. Near a fuzz tie that
  candidate loses to the Gather Merge one on pathkeys anyway (ledgered).

Movement: TPC-H PLAN-PARITY match 8 → 9 (Q5 = PG); `sort-strategy` 6 → 5.
Q12's first divergence moves from depth 2 to depth 4 (a join method under
the aggregate). TPC-DS `sort-strategy` goes 50 → 48 at SF0.25 and 51 → 49 at
SF1: Q62 and Q99 lose the category. Values are identical everywhere, and the
fire set has no introduced timeouts.

Q4 stays divergent for a different reason. goopg's per-worker seed is
`parallelSeedCost`, which divides the whole run cost by the divisor, while
upstream leaves the I/O undivided. It prices Q4's input at about 25.7k
against PG's 68.9k, so the same ~300-unit worker sort is 1.15% of goopg's
base against PG's 0.3%, just outside the fuzz. That is the approximation
the function's own comment names (ledgered). Evidence:
`analysis/m0146/m0146-0005/slice32/`.

## Slice 33: M0146-0005ag — the gathered arm's ordered-aggregate rules

TPC-H Q16 groups `count(DISTINCT ps_suppkey)`. PG elects `GroupAggregate ->
Gather Merge -> Sort (p_brand, p_type, p_size, ps_suppkey)`. Two upstream
rules produce it. `GROUPING_CAN_USE_HASH` requires `numOrderedAggs == 0`
(planner.c:3846), so a grouping with DISTINCT or ORDER BY aggregates is
never hashed. `adjust_group_pathkeys_for_groupagg` also appends the
aggregate's own keys to `group_pathkeys`, so the input is sorted to serve
both the grouping and the DISTINCT.

goopg's serial grouping arm (groupingpaths.go) already followed both rules
through `presortedAggKeysOrAbsent` and `groupingHashable(agg, presorted)`.
Its parallel twin, the gathered no-split arm in `addPartialAggSplitPath`,
did not. It skipped the sorted family for any special aggregate and always
offered the hashed arm, so Q16 elected `HashAggregate -> Gather`.

- **Change.** The gathered arm now takes the serial arm's rules verbatim. With
  presorted keys, both sorted gathered candidates sort to them: the leader
  Sort above the Gather, and the per-worker Sort under a Gather Merge. The
  hashed candidate is not offered. With no special aggregate the keys are
  the group keys, as before. A special aggregate without usable presorted
  keys still gets the hashed candidate alone, the serial arm's documented
  conservatism.

Movement: TPC-H Q16's first divergence moves from depth 1
(aggregation-strategy) to depth 5 (PG's `Parallel Index Only Scan` on
`partsupp`). TPC-H `aggregation-strategy` 5 → 4, `sort-strategy` 5 → 4,
`parallelism` 7 → 6, `parameterisation` 4 → 3, `join-order` 11 → 10. TPC-DS
plans are unchanged at both scales (no fires). Values are identical.
Evidence: `analysis/m0146/m0146-0005/slice33/`.

## Slice 34: M0146-0005ah — passthrough columns belong to the aggregate's input target

TPC-H Q18 groups by `c_name, c_custkey, o_orderkey, o_orderdate,
o_totalprice`. PG reduces the key to `c_custkey, o_orderkey` through
primary-key functional dependency (`remove_useless_groupby_columns`) and
elects `HashAggregate -> Gather`. goopg performs the same reduction and
carries the dropped columns as `Aggregate.Passthrough`, yet elected
`GroupAggregate -> Gather Merge -> Sort`. Its hashed candidates cost about
1.6M over a 335k input, while PG's HashAggregate adds about 6k.

- **Cause.** `groupAggregateInputNames` declined — returned "unknown" — for
  any aggregate with a Passthrough, so `InputTarget` was never known.
  `aggInputWidth` then sized the hash entries and the sort rows from the
  full 33-column join row (width 1654). At 509k groups that overflows the
  1GB hash budget and charges a spill. PG sizes the same entries from the
  narrowed input target (width 52).
- **Change.** Passthrough expressions are enumerated like group keys. The
  applying cut (`narrowAggregateInput`) already rewrote and gate-checked
  them (`keepPreservesExprList`); only the derivation declined. An
  unenumerable passthrough (an outer reference, say) still declines.
  `TestAggregateInputTargetUnknownOnPassthrough` becomes
  `TestAggregateInputTargetKeepsPassthrough`.

Movement: Q18's first divergence moves from depth 1 (aggregation-strategy)
to depth 3 (PG's Parallel Hash Join against a nested loop under the Gather).
TPC-H `aggregation-strategy` 4 → 3, `sort-strategy` 4 → 3, `parallelism`
6 → 5, `join-method` 4 → 5. TPC-DS plans are unchanged at both scales, and
values are identical. In the regress runner, 7 cases (including
`functional_deps`) show one change against HEAD: the row order of one
unordered `join.sql` query, with the same row set. Evidence:
`analysis/m0146/m0146-0005/slice34/`.

## Slice 35: M0146-0005ai — a Gather's own Filter is printed

Checking TPC-H Q20 (first divergence: `parallelism`), the goopg plan showed no
`ps_availqty > (SubPlan)` qual and no `lineitem` at all. The rows were right:
a self-check through an explicit pre-aggregated join returns the same 101
rows, and dropping the predicate returns 271. The two engines' loads
differ, so their row counts cannot be compared. The qual was executed but
invisible. `EXPLAIN (FORMAT JSON)` put it as a `Filter` on the Gather node:
goopg evaluates the parallel-restricted correlated SubPlan in the leader,
above the Gather. The text renderer's Gather arm printed only `Workers
Planned`.

- **Change.** explain.c's `T_Gather` and `T_GatherMerge` arms print
  `show_scan_qual(plan->qual, "Filter")` before `Workers Planned`, and
  under ANALYZE `Rows Removed by Filter` directly after it. goopg's Gather
  and Gather Merge text arms now print the folded Filter in that position,
  and the SubPlan tree renders with it. The ANALYZE walk appends the
  removed-rows line after the plain details, so it is moved in front of the
  node's `Workers Planned` line (`moveBeforeLastDetail`).
- **What it exposes.** Q20's SubPlan is now compared. PG probes `lineitem`
  with an Index Scan inside the SubPlan, and goopg uses a Bitmap Heap
  Scan, so TPC-H `scan-type` rises 9 → 10 and `parameterisation` 2 → 3.
  These are real divergences that were hidden, not regressions. First
  divergences are unchanged, and so are TPC-DS plans.
- **Q20's real divergence** is the placement itself. PG keeps the
  parallel-restricted SubPlan qual on the serial parameterized inner scan
  and runs no Gather there. goopg parallelizes the join and filters above
  the Gather (ledgered).

Found on the way (both ledgered, one filed):

- **Q19/Q21 `qual-placement`.** PG enforces every join clause movable to a
  parameterized inner path inside that inner scan: `ppi_clauses`
  (`get_baserel_parampathinfo`) become the scan's Filter. goopg keeps them
  as the nested loop's Join Filter. Filed M0146-0005aj. Doing this needs a
  Memoize cache key per referenced outer value.
- **Q22's InitPlan.** PG evaluates it once in the leader and passes the
  value to the workers, which lets the InitPlan itself run parallel. goopg
  evaluates it inside the workers' Filter, and so refuses a parallel
  aggregate there (`gate=statement`).

Evidence: `analysis/m0146/m0146-0005/slice35/`.

## Slice 36: M0146-0005aj — a parameterized nested loop's residual is the probe's Filter

PG enforces every join clause movable to a parameterized inner path inside
that inner scan. `get_baserel_parampathinfo` collects them as `ppi_clauses`,
and `create_scan_plan` appends them to the scan qual after its own
restriction quals. They print as the inner scan's `Filter:`, with the outer
side's Vars as Params that deparse prefixed. goopg printed the same clauses
as the nested loop's `Filter:` or `Join Filter:`. That is the
`qual-placement` class: TPC-H Q19's OR clause, Q21's `l3.l_suppkey <>
l1.l_suppkey`, TPC-DS Q94's `ws1.ws_warehouse_sk <> ws_warehouse_sk`.

- **Why rendering is the faithful fix.** Both parameterized nested-loop
  forms (`NestedLoopIndexJoin`, and the Lateral nested-loop `Join` over a
  probe) evaluate `Predicate` once per candidate row the probe returns for
  the current outer row, before the join emits or counts a match. That is
  exactly where PG's scan qual runs. The residual already is the probe's
  `ppi_clauses`; only its attribution differed. A new plan field would
  have had to be threaded through every pass that remaps `Predicate`
  coordinates, for no semantic change.
- **The rule** (`explain_nli_paramqual.go`, `innerParamQual`). The whole
  residual renders under the inner scan only when all of these hold:
  - the inner is a bare IndexScan, BitmapHeapScan or IndexOnlyScan with
    no Memoize in between (with a cache the residual runs above it here,
    while PG would key the cache on the extra values);
  - every conjunct reads the inner relation;
  - every conjunct references only the probe's own table and the relations
    its index keys are parameterized by. This is PG's `required_outer` test
    (`join_clause_is_movable_into`). TPC-DS Q19's `substr(ca_zip) <>
    substr(s_zip)` stays a Join Filter because the `store` probe is
    parameterized by `store_sales` alone. A key's outer reference with no
    source id is resolved through the outer row's schema.
  - no conjunct is an inner-to-outer column equality. That is an
    equivalence-class clause: PG's `ppi_clauses` carry one per class (the
    one the index uses), and the join re-checks any other member pair as a
    Join Filter (TPC-H Q9's `s_suppkey = l_suppkey`).
- **Rendering details.** Outer columns become correlated references,
  printed prefixed. The scan's own `Cond` leads the combined Filter. Under
  ANALYZE the join's rejection count becomes the inner scan's `Rows
  Removed by Filter`, and the join's `Rows Removed by Join Filter` line is
  dropped.

Movement: TPC-H `qual-placement` 3 → 2 (Q19 moves past to a scan-type
record); TPC-DS `qual-placement` 18 → 17 at SF0.25 and 19 → 18 at SF1 (Q94).
No category rises and no timeouts are introduced. In the regress runner,
one join.sql EXPLAIN moves its clause the PG way (that query's PG plan
removes the self-join, a separate gap).

Remaining (ledgered):

- A residual that mixes an equivalence-class equality with movable
  clauses stays on the join whole, because the executor keeps one
  rejection counter.
- Under ANALYZE, the inner scan's actual `rows` still counts rows before
  the moved qual.
- JSON EXPLAIN renders neither the NLI residual nor `IndexScan.Cond`.
- PG's extra equivalence-class Join Filter (Q21's `orders.o_orderkey =
  l2.l_orderkey`) is not generated.

Evidence: `analysis/m0146/m0146-0005/slice36/`.

## Slice 37: M0146-0005ak — keys a WHERE constant pins; sorted aggregates split over a Gather Merge

TPC-DS Q42/Q52 group and order by `dt.d_year` under `WHERE dt.d_year =
<const>`. PG's `Group Key:` and `Sort Key:` lines omit it. Since PG 16,
`standard_qp_callback` builds the group and sort pathkeys with
`make_pathkeys_for_sortclauses_extended(..., remove_redundant = true)`. A
clause whose equivalence class holds a constant sorts and groups nothing
(`pathkey_is_redundant`), so it leaves `processed_groupClause` and
`sort_pathkeys`.

- **GROUP BY** (`redundantConstGroupKeys`, groupkeyconst.go). A second pass
  after `pruneUselessGroupByColumns` in `buildAggregateStage` drops a
  bare-column key pinned by a top-level WHERE `col = const`. The column
  stays in the output as a passthrough, the same mechanism
  functional-dependency pruning uses. It declines in three cases:
  - every key is pinned (PG then runs a keyless GroupAggregate that
    returns no row on empty input, which goopg's zero-key aggregate
    cannot express);
  - the query has grouping sets or `GROUPING()` calls (existing gate);
  - the equality is under an OR.
- **ORDER BY** (`orderItemPinnedByWhere`). A pinned item is skipped when
  sort keys are built. Items are matched on the written column, and an
  unqualified name that resolved is unambiguous. It declines for grouping
  sets, where a set's non-member columns are NULL above the WHERE (regress
  groupingsets `WHERE x = 1 ... GROUPING SETS (x, y) ORDER BY 1, 2`), and
  for set operations. With every item pinned no Sort is built. With WITH
  TIES every row ties, which is correct because pinned rows are equal.
- **A sorted aggregate split around a plain Gather** (pre-existing run-time
  ERROR, fixed here). Since the row transport (M0146-0003b) a sorted
  finalize needs a merge-ordered child. Two producers still built
  `Finalize GroupAggregate -> Gather`, which failed with "partial-state
  stream is not ordered by group key" (regress `limit.sql`, and `SELECT
  d_moy, count(*) ... GROUP BY d_moy ORDER BY d_moy` on a one-worker scan):
  - `addPartialAggSplitArm` took the serial election's strategy. It is now
    pinned to hashed: that arm is `Finalize -> Gather`, and PG's sorted
    finalize always sits on a Gather Merge, which the sorted-input and
    presorted arms file.
  - The parallel post-pass `splitAggregate` split a sorted aggregate around
    a Gather. It now builds `splitAggregateTransportSortedInput` (Finalize
    GroupAggregate -> Gather Merge -> Partial GroupAggregate -> Sort). It
    cannot become hashed, because the plan above may rely on the sorted
    output order.

Movement: TPC-DS `rendering` 24 → 21 at SF0.25 and 20 → 17 at SF1 (Q42, Q52,
Q81 group and sort lines now match). One cost tie flips at SF1: Q52's
sorted-input split (20331.816) now undercuts the gathered GroupAggregate PG
elects (20331.819) because the pinned key's comparison cost left both, and
SF1 `join-method`, `aggregation-strategy` and `sort-strategy` each rise by
1. TPC-H is unchanged. In the regress runner (10 cases like-for-like):
`limit.sql` returns PG's rows instead of the internal error, and join.sql's
`WHERE fault = 122 ORDER BY fault` loses its Sort, as in PG.

Found on the way and filed as S2, not fixed: **M0146-0034**. `Limit ->
Gather -> Parallel Index Scan` returns rows in scheduling order: `ORDER BY
d_date_sk LIMIT 1` gives the wrong first row in 6 of 12 runs on HEAD. Evidence:
`analysis/m0146/m0146-0005/slice37/`.

Slices 38+ continue in
[m0146-0005-join-order-burndown-3.md](m0146-0005-join-order-burndown-3.md)
(split per the design-doc size rule, D3).
