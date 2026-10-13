# M0146-0014 routing 2026-10-13, group A: Q16, Q42, Q52, Q60, Q32, Q76, Q85 (SF0.25)

Inputs: `goopg-sf025.plans.txt`, `pg-sf025.plans.txt` and
`first-divergence-sf025.txt` in this directory. This is a read-only analysis
of the plan text; no server was run. These records were previously routed to
B8 or B-15, which are both retired or ported now, so every route below is new.

Two relation-size offsets show up in every query. They are common to both
shapes unless the text says otherwise:

- goopg prices `item` from 1242 blocks, PG from 1284 (Q32 item Seq Scan:
  1467.00 vs 1509.00, same 18000 rows × 0.0125).
- `date_dim` is 19 blocks smaller in goopg (Parallel Seq Scan 2049.55 vs
  2068.55).
- `catalog_sales` is 57 blocks smaller (10521.57 vs 10578.57).
- `customer_address` is 48 blocks smaller (1786.53 vs 1834.53).

| Query | First divergence | Class | Route |
|---|---|---|---|
| Q16 | d4: PG NL(call\_center ⋈ Materialize(Gather)) \| goopg Gather with the call\_center probe inside | COSTTIE (0.006%) | M0146-0014 waived residual; settle with a PG session trace |
| Q42 | d6 join order: PG ss⋈date\_dim first \| goopg ss⋈item first | COSTTIE driven by RELPAGES | M0146-0009h (relation-size parity, item and date\_dim) |
| Q52 | same as Q42 (goopg plan byte-identical to Q42's) | COSTTIE driven by RELPAGES | M0146-0009h |
| Q60 | d1: PG Incremental Sort \| goopg full Sort | MECHANISM (extra path goopg offers) | NEW: create\_ordered\_paths rule, planner.c:5363-5379 |
| Q32 | d2: PG Hash Join (cs⋈dd Gather, item hash) \| goopg NL item→cs bitmap→dd | COSTTERM (correlated SubPlan join filter not charged on the NL) | NEW (also covers Q92 SF0.25) |
| Q76 | d6 cs arm: PG NL item\_pkey probe \| goopg Parallel Hash Join on item | ESTIMATE (cs\_bill\_customer\_sk IS NULL 585 vs 512 per worker), with RELPAGES as the residual | M0146-0009 (statistics; ANALYZE sample) |
| Q85 | d2: PG Partial GroupAgg → Gather Merge → Finalize \| goopg Gather → Sort → GroupAgg | COSTTIE (≈0.08 of 8416, 0.001%) | M0146-0014 waived residual (same family as Q19/Q40/Q71) |

## Q16: COSTTIE

**Shapes.** Below the divergence the join tree matches: the Parallel Hash
Join cs1⋈date\_dim, the customer\_address probe, the anti join with cr1 and the
semi join with cs2. The only difference is where call\_center goes:

- **PG** keeps call\_center outside the Gather: `NL (Seq Scan call_center 1.10,
  rows=6) ⋈ Materialize(Gather 17032.08)`, total 17033.36.
- **goopg** probes `call_center_pkey` per row inside the workers: Index Scan
  0.13..0.16, so the NL over the anti join goes 13253.16 → 13253.32. The semi
  join is 15956.96 and the Gather 16957.26.

**Margin in each engine.**

- **goopg.** Rebuilding the outside shape from goopg's own numbers: semi
  without cc ≈ 15956.79, Gather +1000.2, then + call\_center 1.10 + PG's
  NL/Materialize overhead 0.17 ≈ 16958.4. That is about 1.1 above the chosen
  16957.26 (0.006%).
- **PG.** The same rebuild on PG's numbers gives the inside shape at about
  16031.88 + 0.17 + 1000.2 ≈ 17032.25, which is about 1.1 *below* PG's elected
  17033.36.
- PG's `call_center_pkey` probe is amortised over loop\_count = cs1 rows
  (360k) by get\_loop\_count, so it should price ≈0.16 just as goopg does.
- So PG either adds a term of at least about 1.1 to the inside variant that the
  plan text does not show, or it builds that variant differently.
  `consider_parallel_nestloop` (joinpath.c:2160) does offer
  `cheapest_parameterized_paths`.

**Classification.** COSTTIE: both alternatives are within 0.01% in both
engines, but the reason for PG's direction is not visible in the plan text.
The relation-size offsets (cs1 −57, date\_dim −19) are common to both shapes.

**Side note.** goopg's top Gather prints rows=3 (its final joinrel rows) where
PG's joinrel rows are 2. Both shapes share that rel, so it does not decide this
record.

**Route.** M0146-0014 waived residual (COSTTIE).

**First step.** On :65438 `tpcds025`, run a session-only `SET enable_material
= off` and EXPLAIN Q16. The outside shape then has to rescan the Gather 6
times, so PG prints its best inside-Gather alternative with exact costs. Diff
that node by node against goopg's 16957.26 tree to find the term PG adds.

## Q42: COSTTIE driven by RELPAGES

**Shapes.**

- **PG:** Parallel Hash Join (ss ⋈ date\_dim 18 rows) 17936.56, rows=91, then
  an NL probe of `item_pkey` at 0.31 per row. NL 17965.22, Gather 18965.72.
- **goopg:** Parallel Hash Join (ss ⋈ item, i\_manager\_id=1, 189 rows)
  17242.61, rows=4141, then NL Memoize(`date_dim_pkey`) at 0.30..0.33. NL
  17936.41, Gather 18936.91.

**Margin in goopg.** PG's own Parallel Hash Join overhead is
17936.56 − 15258.18 − 2068.55 = 609.83. Applying it to goopg's inputs gives
goopg's version of PG's shape: 15256.18 + 2049.55 + 609.83 + 91×0.31 (+cpu)
≈ 17944.2. That is only about 7.8 (0.04%) above goopg's elected 17936.41.

**Margin in PG.** goopg's shape priced at PG's relation sizes is about
17936.41 + 2 (ss) + 42 (item 1284 vs 1242 blocks) ≈ 17980. That loses to PG's
17965.22 by about 15.

- PG's shape pays only +19 for date\_dim, while goopg's shape pays +42 for item.
  The net swing of about 23 is larger than goopg's 7.8 margin.
- Memoize pricing matches PG: in the matching Q55 both engines show Memoize
  0.30..0.33 over `date_dim_pkey`.

**Classification.** COSTTIE driven by RELPAGES: a 0.04% tie in goopg's own
model, flipped by the item and date\_dim block counts.

**Route.** M0146-0009h (relation-size parity).

**First step.** Compare `pg_relation_size(oid)/8192` and `relpages` for
item, date\_dim, store\_sales, catalog\_sales and customer\_address on :65437
and :65438 `tpcds025`.

- goopg's `estimateRelSize` (internal/optimizer/relsize.go:453) uses the live
  block count, as upstream does (`curpages = RelationGetNumberOfBlocks`,
  table\_block\_relation\_estimate\_size in tableam.c).
- So a 1242-vs-1284 difference is a real heap-size gap, not a planner bug.
- The 2026-10-04 ledger row says "relpages already match through the extension
  grid"; these plans contradict that for SF0.25 item.

## Q52: COSTTIE driven by RELPAGES (same as Q42)

goopg's plan is byte-identical to its Q42 plan (Limit 18937.14), because both
queries filter `i_manager_id = 1`.

- PG: ss⋈dd 17936.56 rows=94 (d\_moy=12, d\_year=2000), NL 17966.16.
- goopg's PG-shape rebuild is about 15256.18 + 2049.55 + 609.83 + 94×0.31 ≈
  17945.1, against the elected 17936.41 (0.05%).
- In PG, goopg's shape is about 17980 against 17966.16.
- The same item (+42) / date\_dim (+19) swing flips it.

**Route and first step.** Same as Q42 (M0146-0009h). Q42 and Q52 should move
together once the item and date\_dim block counts match.

## Q60: MECHANISM (goopg offers a full Sort that PG never builds)

**Shapes.** All three UNION ALL arms and the Merge Append match PG
structurally. The divergence is the top ORDER BY over the GroupAggregate,
which is already sorted on the first key `i_item_id` (presorted\_keys = 1):

- **PG:** Incremental Sort 38095.70..38099.26 over GroupAggregate total
  38097.55.
- **goopg:** full Sort 37201.82..37201.91 over GroupAggregate total 37200.89.

**Cost comparison.**

- Added by PG's incremental sort: +1.71 total.
- Added by goopg's full sort: +1.02 total. A full sort of 38 rows in PG would
  add about 1.09 (0.995 comparison + 0.095 run).
- So the full Sort is about 0.6 cheaper on total in both engines (0.002%), and
  goopg elects it on total cost because LIMIT 100 is above the 36–38 rows.

**Why PG still uses Incremental Sort.** PG never builds the full Sort in this
case. `create_ordered_paths` (planner.c:5363-5379) says:

> We've no need to consider both a sort and incremental sort. We'll just do a
> sort if there are no presorted keys and an incremental sort when there are
> presorted keys.

goopg's `addOrderedPaths` always offers arm 2, a full Sort over the seed. Arm 3,
`addIncrementalSortPaths` (internal/optimizer/incrementalsortpaths.go), is
added alongside it rather than replacing it.

**Classification.** MECHANISM (an extra path goopg generates). Both
candidates are within 0.002%, but the cost tie is not the reason: PG's rule is
structural.

**Route.** NEW. When the seed (or any input path) has `presorted_keys > 0`
and `enable_incremental_sort` is on, offer only the Incremental Sort.
create\_ordered\_paths applies this at planner.c:5363-5379 and again in its
partial-path loop from :5401.

**First step.**

- In upperordered.go `addOrderedPaths`, compute
  `pathkeysCountContainedIn(seed.keys, sortPathkeys)` and skip arm 2 when
  nCommon > 0.
- Re-run Q60 and Q4: Q4 SF0.25 carried the same "Incremental Sort vs Sort"
  COSTTIE on 2026-10-09 and may be this rule too.
- Add the sf025 sweep, because Incremental Sort becomes mandatory on more
  shapes.

## Q32: COSTTERM (correlated SubPlan join filter not charged on goopg's nested loop)

**Shapes.**

- **PG:** Hash Join, with outer Gather(cs⋈dd) 13995.66 rows=427 and inner Hash
  over the item Seq Scan 1509.00 rows=16. Join Filter
  `cs_ext_discount_amt > (SubPlan 1)`, SubPlan 280.92 per call. Total
  15505.98.
- **goopg:** NL(item 1467.00 ⋈ Bitmap Heap Scan cs 89.61 per probe, rows=320)
  2904.33, then an NL `date_dim_pkey` probe 0.33. The SubPlan qual is a
  post-join `Filter:` on that upper NL, SubPlan 268.79 per call. Total
  3010.71, **one fifth of PG's**.

**What goopg charges for the SubPlan.** The upper NL adds
3010.71 − 2904.33 = 106.38, which is 320 × 0.33 date\_dim probes plus cpu.
**Nothing is charged for evaluating the SubPlan**, although it runs once per
joined row: 320 × 268.79 ≈ 86k.

**Why PG must charge it.** Priced with PG's own components, goopg's shape costs
only about 1509 + 16×93.48 + 320×0.33 ≈ 3110. PG still elects a plan five times
more expensive, so PG must charge the SubPlan per tuple on that shape:

- final\_cost\_nestloop (costsize.c, near the 3380 rows block) adds
  `cpu_per_tuple = cpu_tuple_cost + restrict_qual_cost.per_tuple` per
  `ntuples`.
- cost\_qual\_eval\_walker (costsize.c:5008-5019) adds the SubPlan's
  `per_call_cost` from cost\_subplan. Here that is startup + run of the
  correlated Aggregate (costsize.c:4586-4618).
- The Hash Join pays this only on hash-matched tuples (approx\_tuple\_count,
  costsize.c:4504-4515). That keeps its charge small: about 1.1 run above its
  inputs here.
- I could not reconstruct PG's exact Hash Join qual charge from the text
  (Q92's remainder is about 1.33 per matched tuple). That figure should be
  confirmed by trace. It does not change the direction.

**Qual placement is suspect too.** The qual references only cs and item, so in
PG it is a join clause of cs⋈item. goopg hoists it above the date\_dim join as
a `Filter:`, which suggests it is attached outside the costed
joinrestrictinfo. This is the same kind of uncosted hoist as the 2026-10-09 Q6
Gather-Filter finding.

**Classification.** COSTTERM: the join-level SubPlan qual cost is missing from
goopg's join path cost. M0146-0005di only made scan-level `qualEvalOps` charge
`cost_subplan`.

**Route.** NEW. It covers Q32 and Q92 SF0.25, which shows the same
fingerprint: goopg's NL 2277.04 − 2221.05 = 55.99 = 160 × 0.35 probes and 0 for
SubPlan 138.69, against PG's 8959.93.

**First step.** Run a goopg DPPATH trace on the {item, catalog\_sales} joinrel.
Check whether the NL/HJ join cost includes `subPlanCostOps` for the correlated
conjunct. If it does not, charge `per_call_cost × ntuples` (NL) or
`× hashjointuples` (HJ), and attach the qual at the cs⋈item join instead of as
a post-join Filter.

## Q76: ESTIMATE (catalog_sales IS NULL row estimate), RELPAGES residual

**Shapes.** In the cs arm of the Parallel Append, the NL(cs ⋈ date\_dim probe)
matches. The item join differs:

- **PG:** NL `item_pkey` probe, 2.91 per row × 509 rows, total 13795.31.
- **goopg:** Parallel Hash Join on item (Parallel Seq Scan 1347.88), total
  13813.91 at 582 rows.

**Row estimates.** goopg estimates `cs_bill_customer_sk IS NULL` at 585 rows
per worker; PG estimates 512. That is null\_frac about 0.0050 vs 0.0044 of 360k
rows, and the joined rows are 582 vs 509.

**Margin in goopg.**

- At its own 582 rows, the NL alternative is about 12332.15 + 582×2.91
  ≈ 14026. That loses to the Parallel Hash Join (13813.91) by about 210 (1.5%).
- At PG's 512/509 rows, goopg's arm becomes NL ≈ 10521.57 + 512×3.095 +
  509×2.91 ≈ 13587.4 against Parallel Hash Join ≈ 12106 + 1347.88 + 133.7
  ≈ 13587.8. That is a dead heat that the NL wins by about 0.4.

**Margin in PG.** PG's own Parallel Hash Join alternative is about 12313.97 +
1389.88 + ~134 ≈ 13838. It loses by about 42, which is exactly the item block
gap (1284 vs 1242).

**Classification.** ESTIMATE. The +14% IS NULL estimate on catalog\_sales (a
null\_frac from the ANALYZE sample) moves the NL cost linearly while the hash
cost stays flat. RELPAGES then decides what is left of the tie. The other arms
show the same sampling noise: ss 10465 vs 10380, ws 15 vs 18.

**Route.** M0146-0009 (statistics: the sample-derived null\_frac of
catalog\_sales.cs\_bill\_customer\_sk), plus M0146-0009h for item.

**First step.** Compare `pg_stats.null_frac` for
`catalog_sales.cs_bill_customer_sk` on both clusters. If a single ANALYZE
sample explains it (about ±2σ at 30000 rows), record it as sampling noise or
pin PG's stats in the capture clone.

**Adjacent finding (not this record).** goopg's Parallel Append costs
1000.58..14166.13, which is below its own first child (17941.59).

- PG's cost\_append parallel-aware branch (costsize.c ~2340-2390) sums the
  partial children: 17999.50 + 13795.31 + 5767.17 + 0.005×10454 = 37614.25.
- goopg's whole Q76 costs 15612 against PG's 39026.
- This does not drive the per-arm choice. It would distort any decision
  between an Append and an alternative. See
  internal/optimizer/parallelappendorder.go.

## Q85: COSTTIE

**Shapes.** The whole join tree matches; costs are lower in goopg because of
the customer\_address block count and the cd probes (4.22 vs 4.46). The two
engines aggregate differently:

- **PG:** Sort → Partial GroupAggregate (7416.21) → Gather Merge
  8416.19..8416.45 → Finalize 8416.50.
  This adds 1000.33 above the partial NL (7416.17).
- **goopg:** Gather (8314.91) → Sort → GroupAggregate 8314.96.
  This adds 1000.25 above its partial NL (7314.71).
- Applying goopg's +1000.25 to PG's NL puts the plain shape at about 8416.42,
  about 0.08 below PG's Finalize 8416.50 (0.001%).

**How PG's add\_path should break this tie.**

- Both shapes are equal within STD\_FUZZ, and have the same pathkeys and rows=1.
- The 1e-10 comparison then gives COSTS\_DIFFERENT: the plain shape has the
  lower total, Finalize has the lower startup (8416.19 vs about 8416.38).
- In that case PG keeps the path that was added first (pathnode.c:560-576).
- add\_paths\_to\_grouping\_rel adds the plain paths before the Finalize paths.
  So the only way PG ends on Finalize is if PG prices the plain shape at least
  0.08 higher than this rebuild.

**Classification.** COSTTIE, the same family as the Q19/Q40/Q71 "Finalize vs
plain GroupAggregate" residuals.

**Route.** M0146-0014 waived residual (COSTTIE).

**First step, if it is ever pursued.** Use the goopg DPPATH trace at the
GROUP\_AGG rel to print both candidates' startup and total. Check whether
goopg's Finalize candidate exists and is within 0.1 of PG's 8416.50 − 101.46.

## Adjacent records seen while reading (not assigned)

- **Q92 SF0.25:** same COSTTERM as Q32 (described in Q32 above). It should
  move from the M0146-0130 MECHANISM route to the Q32 route.
- **Q4 SF0.25** (2026-10-09 COSTTIE, Incremental Sort vs Sort) is probably the
  same planner.c:5363 rule as Q60.
