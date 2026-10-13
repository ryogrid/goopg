# M0146-0014 routing 2026-10-13: group B (Q5, Q6, Q24, Q64, Q70, Q92), SF0.25

This is a read-only plan-text analysis of `goopg-sf025.plans.txt` and `pg-sf025.plans.txt` in this
directory. The records come from `first-divergence-sf025.txt`. Four of these queries had earlier
routes that have since landed (M0146-0130, 0132, 0134 and 0140), and in each the first divergence
has moved. Q24 is a new record.

| Query | Class | Route |
|---|---|---|
| Q5 | RENDERING | M0146-0042 (Var deparse above an Append whose first child is a SubqueryScan) |
| Q6 | RENDERING | M0146-0042 (PARAM_EXEC deparse, `$0` vs `i.i_category`) |
| Q24 | COSTTERM (input: allvisfrac) | NEW: the heap-fetch term of a parallel index-only scan on store_returns (cost_index allvisfrac) |
| Q64 | COSTTIE (GEQO) | NEW/low: GEQO PRNG fidelity (geqo_random.c / pg_prng) — or waive as a COSTTIE residual |
| Q70 | MISSING-PATH (probable) | NEW: Hash Semi Join over the tmp1 subquery rel is not filed (hash_inner_and_outer, JOIN_SEMI) |
| Q92 | COSTTERM / MECHANISM | NEW (regression after M0146-0130): the correlated SubPlan conjunct lands as an unpriced top Filter |

---

## Q5: depth 8 under Gather, PG Hash Join Inner vs goopg Hash Join Inner (`join-order`)

- **The shape is identical.** Every node kind matches in all three arms (ssr, csr and wsr). The record
  fires in the wsr arm, on the Hash Cond text of the Hash Join above the Parallel Append:
  - PG: `("*SELECT* 2".wsr_web_site_sk = web_site.web_site_sk)`
  - goopg: `(web_sales_1.ws_web_site_sk = web_site.web_site_sk)`

  The Parallel Hash Join below it differs the same way: PG prints `"*SELECT* 2".date_sk` and goopg
  prints `web_returns.wr_returned_date_sk`. The script's `qual_signature` sees a different relation set
  and calls this `join-order`.
- **Cause.** In PG's `set_deparse_plan` (ruleutils.c:5152), the outer_plan of an Append is its first
  child, here `Subquery Scan on "*SELECT* 2"`. `get_variable` resolves the Var against that scan's RTE
  and prints the alias plus the eref column name. goopg instead descends through the SubqueryScan into
  the Hash Right Join below it. This is the same RENDERING record as the SF1 Q5 record, which is
  already routed to M0146-0042 (held).
- **Class: RENDERING.** Route: **M0146-0042**. First step: in goopg's EXPLAIN Var deparse, stop at a
  SubqueryScan and print `<rte alias>.<subquery eref colname>`, as `get_variable` does for an RTE_SUBQUERY
  scan.
- **Side note, a cost gap that does not change the shape.** The wsr Gather costs 20991.70 in goopg and
  17642.02 in PG. Most of the gap is in `Hash Right Join` (web_sales ⋈ web_returns on 2 keys):
  - Above its outer plus startup, goopg adds 11325.71 − 780.00 − 6496.56 = **4049.15**. PG adds
    8268.33 − 780.00 − 6543.56 = **944.77**.
  - goopg's excess over the 2-clause hash CPU of 899.78 is about 2700. At cpu_tuple_cost that is about
    270k `hashjointuples`, which is about 179956 × 17920 / ~12k. That fits a selectivity taken from one
    hash clause only (no product over both keys), or a missed inner-unique branch: `web_returns_pkey`
    on (wr_item_sk, wr_order_number) makes the inner unique.
  - The `Subquery Scan "*SELECT* 2"` also charges 0.04 per row (+716.8), where PG charges 0.01 (+179.2).

  This is worth its own COSTTERM probe (`approx_tuple_count` / `final_cost_hashjoin` inner_unique for
  JOIN_RIGHT), but it does not cause this record.

## Q6: depth 7 under Aggregate, Seq Scan on item j (`parameterisation`)

- **The plans are node-for-node identical**, including the M0146-0130 item_pkey probe that now holds the
  SubPlan Filter. The only difference is SubPlan 2's scan filter:
  - goopg: `Filter: (i_category = $0)`
  - PG: `Filter: (i_category = i.i_category)`

  `is_parameterised` / `qual_signature` reads that as `parameterisation`.
- **Cause.** PG's `get_parameter` (ruleutils.c:8687) calls `find_param_referent` (ruleutils.c:8455). It
  maps a PARAM_EXEC that a SubPlan's parParam supplies back to the outer Var and prints that Var.
  goopg prints the raw `$n`.
- **Costs.** The totals differ (goopg 418801.58, PG 430178.85), but the difference is per-call price,
  not shape. The SubPlan's `Seq Scan on item j` costs 1467.00 in goopg and 1509.00 in PG: item has 42
  more relpages in PG. 42 × ~270 calls ≈ 11.3k, which is the whole 11377 gap.
- **Class: RENDERING.** Route: **M0146-0042**, a new slice: "PARAM_EXEC → referent deparse
  (find_param_referent)". First step: in goopg's EXPLAIN expression deparser, map a SubPlan's
  ParParam / ExecParamRef back to the outer Var expression it was lowered from (the sublinkHandle
  params), then re-run the Q6 fire-set diff.

## Q24: depth 9 under Hash Join, PG Parallel Seq Scan on store_returns vs goopg Parallel Index Only Scan store_returns_pkey (`scan-type`)

- **Above the divergence the shape is identical.** The outer of the Parallel Hash Join on
  (sr_ticket_number, sr_item_sk) differs:

  | Engine | Outer scan | Cost | Rows | Divisor / workers |
  |---|---|---|---|---|
  | goopg | Parallel Index Only Scan | 0.29..1441.32 | 29865 | 2.4 (2 workers) |
  | PG | Parallel Seq Scan | 1653.62 | 42162 | 1.7 (1 worker) |

  Both row counts are 71675 / divisor. The join totals follow: 18015.43 vs 18322.26. The Gather's
  `Workers Planned` is 2 vs 1.
- **How the worker counts arise.** PG also passes `rand_heap_pages = -1` for a partial index-only scan
  (costsize.c:767). The worker count therefore comes from index pages, through the same log3 ladder in
  both engines. goopg's index is 196 pages, which is at least 192, so it gets 2 workers. The worker
  count is not the difference.
- **Decomposing goopg's 1441.32:**
  - descent startup 0.29
  - index tuples 71675 × 0.005 = 358.38
  - index I/O 196 pages × 4.0 = 784.00
  - cpu_tuple 71675 × 0.01 / 2.4 = 298.65
  - **zero heap-fetch I/O**, so goopg treats store_returns as fully all-visible

  For PG to keep its 1653.62 Parallel Seq Scan, PG's partial IOS must cost at least about 212 more.
  For a (int4, int4) key, PG's index size is about the same (16-byte tuples at a 90% leaf fill gives
  about 196 pages). The extra cost therefore has to be heap fetches, `ceil(pages × (1 − allvisfrac))`
  in `cost_index`. That means PG's relallvisible for store_returns is lower than goopg's.
- **Calibration from other full index-only scans in the same files** (same rows, PG costs more):

  | Index | Rows | goopg | PG | Difference |
  |---|---|---|---|---|
  | item_pkey | 18000 | 430.29 | 531.08 | +100.79 |
  | customer_pkey | 100000 | 2372.29 | 2627.32 | +255.03 |

  Neither difference is a multiple of 4, so both include heap fetches. Both also include a second,
  separate gap: goopg's int4 pkeys price at 40 and 218 index pages, about 458 tuples per page. That is
  more than PG's maximum of 407 for MAXALIGNed 16-byte int4 tuples. PG would have about 52 and about
  275 pages.
- **Why the record is new.** Retiring the ×2 index-probe multiplier is what brought goopg's IOS under
  the 1617.62 seq scan.
- **Class: COSTTERM**, with the input as the likely source: the IOS heap-fetch term (allvisfrac /
  relallvisible) on the same node with the same rows.
- **Route: NEW.** PG reference: `cost_index` allvisfrac (costsize.c:696-747) and `estimate_rel_size`
  (plancat.c) `allvisfrac = relallvisible / curpages`. goopg reference: `relAllVisibleFraction`
  (internal/optimizer/pathindexonly.go:460).
- **First step** (read-only SELECTs on the reference clusters):
  1. Compare `relpages, relallvisible` for store_returns, item and customer in pg_class on :65438
     (db tpcds025) and on goopg :65437.
  2. On PG, run `SET enable_seqscan=off; EXPLAIN SELECT sr_item_sk, sr_ticket_number FROM store_returns`
     to read PG's partial IOS price.
  3. If goopg reports VM coverage that PG lacks, fix the source of goopg's relallvisible (VACUUM's
     VM-set accounting versus PG's).
  4. File the int4 index-tuple padding gap (40 vs ~52 pages) separately.

## Q64: depth 5 under Nested Loop, PG Nested Loop Inner vs goopg Nested Loop Inner (`join-order`)

- **The M0146-0134 fix holds.** Both plans now have Incremental Sort (Presorted Key i_item_sk) over the
  same spine, up to and including the `store_sales_pkey` probe at rows=1.
- **What diverges is the order of the 14 rows=1 dimension probes above that spine.**
  - PG: ad1, cd1, store, d1, customer, d3, d2, hd1, hd2, ib2, ib1, promotion, cd2, ad2
  - goopg: d1, store, ad1, promotion, customer, d2, cd2, d3, cd1, ad2, hd2, ib2, hd1, ib1
- **It is an exact tie.** With every input at rows=1, a left-deep chain of index probes costs the sum of
  the inner probe prices whatever the order. Probe-tail sums: PG 13868.55 → 13874.00 = **5.45**; goopg
  13803.80 → 13809.19 = **5.39**. That is 0.04% of the total, and each engine's alternatives tie
  exactly in its own model.
- **Why the order is arbitrary.** The cross_sales FROM list has 18 relations. That is at least
  `geqo_threshold` = 12 and is not split by from_collapse_limit (`make_rel_from_joinlist`,
  allpaths.c:3420), so PG plans it with **GEQO**. The tail order is whatever the winning chromosome
  carried. goopg has GEQO, but its PRNG is a 64-bit LCG with state 1 (`geqo.go` `newGeqoRNG`), not PG's
  `pg_prng` xoroshiro128** seeded by `geqo_set_seed` (geqo_random.c:20). Its fitness values also differ
  (13809 vs 13874), so the pool sort and selection diverge in any case.
- **Class: COSTTIE (GEQO tie).** Route: waive as an M0146-0014 COSTTIE residual. Optionally file a
  low-priority task: port geqo_random.c plus pg_prng (`pg_prng_fseed`, `pg_prng_double`) so the GA's
  random stream is bit-identical. Full parity would also need exact fitness parity, so this alone may
  not close the record. First step: confirm that goopg's Q64 search actually enters `geqoSearch`
  (nrels=18), then log the winning tour from both engines (PG: `geqo` debug build / `GEQO_DEBUG`).

## Q70: depth 6 under MixedAggregate, PG Hash Join Left Semi vs goopg Nested Loop Inner (`join-method`)

The IN-sublink over `tmp1` (GROUP BY s_state, so it is provably distinct on s_state: rows=2, and
neither plan has a Unique node) joins the 3-rel main join.

- **PG: `Hash Semi Join` = 38304.53.**
  - outer: Gather of the main join, 19258.50
  - inner: Hash of Subquery Scan tmp1, 19002.75
  - the join itself adds +43.28
- **goopg: `Nested Loop` (inner join over a unique tmp1) = 38320.63.**
  - outer: tmp1 WindowAgg, 18981.59
  - inner: Materialize over the Gather, 19252.59
  - join filter 2 × 3140 × 0.0125 = 78.5, plus rescan of the Materialize
- **The arithmetic.** PG's arm built from goopg's own inputs would cost about 19236.89 + 18981.59 + ~43
  ≈ **38262**. That is about 59 cheaper than the NL goopg elected. PG's version of goopg's NL would be
  about 19002.73 + 19274.2 + 78.7 + 7.9 ≈ 38363, which loses by 59. Row estimates match (3146 vs 3140,
  tmp1 2 vs 2), so ESTIMATE is ruled out.
- **Class: MISSING-PATH (probable).** goopg does not appear to file a hash join in which the
  sublink-pulled tmp1 subquery rel is the hashed inner (JOIN_SEMI, or JOIN_UNIQUE_INNER with an
  already-unique rel) over the main join's Gather. UNKNOWN until the step below has been run.
- **Route: NEW.** PG reference: `hash_inner_and_outer` (joinpath.c) for JOIN_SEMI, and
  `final_cost_hashjoin`'s semi arm (costsize.c:4479).
- **First step:** instrument `addPath` on the joinrel {store_sales, d1, store, tmp1} for Q70 SF0.25 and
  list every hash candidate (outer, inner, jointype, cost). If none has tmp1 as the hashed inner, find
  the gate that drops it. Candidates are a sub-plan leaf (costSubplanLeaf) that is not offered as a
  Hash inner, or the semi→inner uniquification path. SF1 shows the same record.
- Rendering residuals in the same plan, for M0146-0042: `(grouping + grouping)` vs `GROUPING(store.s_state)
  + ...`, an unqualified `s_state` in the Join Filter, and `CASE ... END` without `ELSE NULL::bpchar`.

## Q92: depth 3 under Aggregate, PG Nested Loop Inner vs goopg Nested Loop Inner (`parameterisation`)

- **The shapes.**
  - PG: `NL( Hash Join[web_sales ⋈ item, Join Filter ws_ext_discount_amt > (SubPlan 1)], date_dim
    probe )` = 8959.91.
  - goopg: `NL( NL[Seq Scan item 16 rows × Bitmap Heap Scan web_sales by i_item_sk], date_dim probe )`,
    with the SubPlan conjunct as a plain `Filter:` on the top NL = **2277.04**.
- **goopg does not charge for the SubPlan at all.** 2221.05 + 160 × 0.35 = 2277.05, the displayed total.
  The SubPlan (138.69..138.70 per call) runs about 160 times, which should add about 22.2k.
- **PG charges it on the inner-unique hash join**, where it is evaluated only on `outer_matched_rows` = 3:
  1509.20 + 6543.56 + 449.89 (hash clause) + 22.50 (bucket walk) + 3 × (0.01 + 0.0025 + 138.70) =
  **8941.29**, matching the displayed 8941.30. `cost_subplan` sets per_call_cost to 138.70
  (costsize.c:4534 ff.; the startup is added per call because the SubPlan is correlated).
- **PG's equivalent of goopg's shape** would pay the SubPlan on about 176 tuples (16 × 11), roughly 24.4k,
  so it loses.
- **The regression.** In the 2026-10-09 capture, goopg had PG's hash join with the SubPlan as a
  `Join Filter` (8994.36). Since M0146-0130 (a6905e365), the conjunct is no longer in the join
  restrictlist of {web_sales, item}. It is applied above the join search, where joinQualPerTuple /
  subPlanJoinQualOps never see it. SF1 shows the same flip: PG Hash Join vs goopg NL.
- **Class: COSTTERM / MECHANISM**, a qual-placement regression that leaves the SubPlan unpriced.
- **Route: NEW, against M0146-0130's follow-up.**
- **PG reference.** `distribute_qual_to_rels` (initsplan.c) places the clause at relids {web_sales, item}
  as a join clause. `cost_qual_eval` → SubPlan `per_call_cost` is charged in `final_cost_hashjoin` /
  `final_cost_nestloop`.
- **First step:** dump Q92's conjunct placement, either from `correlatedScalarSublinkLeaf` / the seam
  that decides between a leaf, a join clause and a top Filter, or from the {item, web_sales} joinrel's
  restrictlist. Then confirm that the elected param-NL path's cost omits `subPlanJoinQualOps`. The fix
  is to keep a two-rel correlated-SubPlan conjunct as a join restrictinfo, so every join path pays
  `joinQualEvalCost`.
