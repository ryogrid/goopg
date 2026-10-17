# M0146-0014 routing 2026-10-09: index-probe, sublink and anti/semi records

Inputs: `goopg-sf025.plans.txt`, `pg-sf025.plans.txt`, `goopg-sf1.plans.txt`,
`pg-sf1.plans.txt` and `first-divergence-{sf025,sf1}.txt` in this
directory. The analysis is read-only plan text; no server was run.

| Query | Scale | First divergence (short) | Cause with cost numbers | Family | Target |
|---|---|---|---|---|---|
| Q32 | SF0.25 | d6 scan-type in SubPlan 1: PG Index Scan date\_dim\_pkey \| goopg Bitmap Heap Scan | Per-probe date\_dim lookup: PG Index Scan 0.29..8.14; goopg Bitmap Heap Scan 8.26..12.27, cheaper than goopg's own 2x-priced index scan (about 16.27). SubPlan Aggregate: 363.62 in goopg, 280.92 in PG. | B8 | M0145-0008ag (owner B8 decision M0146-0068) |
| Q32 | SF1 | d6 parameterisation: PG Memoize → Index Scan date\_dim\_pkey \| goopg Bitmap Heap Scan | PG Memoize 0.30..7.55 over Index Scan 7.54, ×87 probes. goopg Bitmap Heap Scan 12.27 per probe beats its 2x index probe (about 16.27, plus Memoize). SubPlan: 1413.52 in goopg, 996.43 in PG. | B8 | M0145-0008ag (owner B8 decision M0146-0068) |
| Q92 | SF1 | d7 parameterisation: PG Memoize → Index Scan date\_dim\_pkey \| goopg Bitmap Heap Scan | Same as Q32 SF1: PG Memoize 7.96 (Index Scan 7.95) ×44 probes; goopg Bitmap Heap Scan 12.27 per probe beats its 2x index probe. SubPlan: 719.04 in goopg, 518.14 in PG. The upper spine matches PG. | B8 | M0145-0008ag (owner B8 decision M0146-0068) |
| Q6 | SF0.25, SF1 | d3: PG Nested Loop (item probe above Gather Merge) \| goopg Sort over Gather | PG keeps item, with its correlated-SubPlan qual, above the Gather Merge: 1513.82 per probe, total 427656.80 at SF0.25 and 1696053 at SF1. goopg puts item inside the workers and moves the qual to a Gather Filter that is not costed (Gather 18921.80 vs child 17891.04 = setup + tuple cost only). | MECHANISM | NEW: set\_rel\_consider\_parallel / max\_parallel\_hazard\_walker — a correlated SubPlan's PARAM\_EXEC makes the rel and its joinrels parallel-restricted at path generation (no Gather-Filter hoist; cost\_qual\_eval charges the SubPlan) |
| Q83 | SF0.25 | d7 web\_returns arm: PG Hash Semi Join over (wr ⋈ Memoize date\_dim\_pkey) \| goopg Hash Join wr ⋈ Gather(semi chain) | Margin 7472.10 (goopg) vs 7152.14 (PG), 4.5%. PG's arm rests on the Memoize'd date\_dim\_pkey probe (0.68). goopg prices the same probes about 1.24x higher (0.62 vs 0.50 in the cr arm, 0.45 vs 0.41 in the sr arm), so that arm costs about 7490 and loses. | B8 | M0145-0008ag (owner B8 decision M0146-0068) |
| Q83 | SF1 | MATCH | — | — | — |
| Q69 | SF1 | d2: order of the two Nested Loop Left Anti joins (PG web first, goopg catalog first) | Same shape, different order. Doing A first is cheaper iff in\_A(1−s\_B) < in\_B(1−s\_A). PG: 896·0.017 < 1786·0.011. goopg: 1728·0.009 < 866·0.020, using anti selectivities 0.980/0.991 vs PG's 0.983/0.989. The swap costs about 3.5 of 165548 (<0.01%). | COSTTIE | M0146-0014 (waived residual) |
| Q78 | SF1 | d2 join-order of the Merge Left Joins (PG joins catalog first, goopg joins web first) | store\_sales ⋉ store\_returns Nested Loop Anti Join: 30 rows per worker in goopg vs 281 in PG (ss GroupAggregate 120 vs 1124). The others are close (cs 568 vs 661, ws 478 vs 481). With the ss side 9x smaller, goopg merges the 1481-row ws side first. Total cost 150206 vs 137297. | STATS | M0146-0009 (statistics) |
| Q10 | SF0.25, SF1 | MATCH | — | — | — |
| Q35 | SF0.25, SF1 | d2 sort-strategy: PG Incremental Sort (presorted ca\_state, from Gather Merge of customer\_address) \| goopg full Sort | Decided by the LIMIT fraction. PG Limit 1095385..4090274 vs full GroupAggregate 54.67M at SF0.25 (26.88M vs 803M at SF1). goopg's comparable full cost is about 58.6M, close to its chosen 58.33M, but a partially presorted input can never win goopg's fraction gate (getCheapestFractionalPathOrdered). | MECHANISM | NEW: get\_cheapest\_fractional\_path on final\_rel (planner.c:439) — let a GroupAggregate over a presorted-prefix Incremental Sort win the LIMIT fraction (ledgered M0145-0019a residual) |
| Q5 | SF0.25 | d9 csr arm Parallel Append: PG Parallel Seq Scan catalog\_returns \| goopg non-partial Seq Scan (child order flips the Hash Cond rel) | goopg applies the min\_parallel\_table\_scan\_size cutoff to appendrel members (catalog\_returns about 721 pages < 1024): no partial path, so non-partial Seq Scan 1081.66. PG skips the cutoff for non-BASEREL and prices Parallel Seq Scan 933.15. Arm cost: 16357.69 in goopg, 15564.84 in PG. | MECHANISM | NEW: compute\_parallel\_worker (allpaths.c) — the min\_parallel\_table\_scan\_size cutoff applies only to RELOPT\_BASEREL; UNION ALL appendrel members always get a partial path |
| Q5 | SF1 | d8 "join-order" in the wsr arm: same Hash Join, different Hash Cond text | The plans match node for node. goopg deparses the Append-output keys through the `"*SELECT* 2"` Subquery Scan (`web_sales_1.ws_web_site_sk`, `web_returns.wr_returned_date_sk`). PG stops at the kept Subquery Scan (`"*SELECT* 2".wsr_web_site_sk`, `.date_sk`). Costs differ only by relpages and width. | OTHER (EXPLAIN deparse, resolve\_special\_varno) | M0146-0042 (EXPLAIN text-identity) |
| Q77 | SF0.25, SF1 | d5 ss arm: PG GroupAggregate over NL(store\_pkey IOS, Materialize Gather) \| goopg Finalize GroupAggregate | Totals are within 1%. SF0.25: PG 3068.92..19036.55 vs goopg Finalize 18920.27..18926.21 (0.58%). SF1: PG 64325.73 vs about 63970 for PG's partial shape (≈0.55%). PG's add\_path fuzz then keeps the low-startup path. goopg's store\_pkey full scan costs 16.31 vs 12.31 (B8). | COSTTIE | M0146-0014 (waived residual) |
| Q14 | SF0.25 | d8 per arm: PG Index Scan item\_pkey on item \| goopg Bitmap Heap Scan on item | Probe of item for the one cross\_items row: PG Index Scan 0.29..8.30; goopg Bitmap Heap Scan 8.26..12.27, cheaper than its 2x index scan (about 16.27). ss arm: 33.23 in goopg vs 27.75 in PG. Same in the cs and ws arms. | B8 | M0145-0008ag (owner B8 decision M0146-0068) |

## Adjacent record seen while reading (not in this assignment)

- **Q92 SF0.25:** d3 parallelism, PG Nested Loop vs goopg Gather. goopg runs
  the Hash Join web\_sales ⋈ item, whose Join Filter holds the correlated
  SubPlan, inside a 2-worker Gather. PG's plan is serial, total 8959.93.
  - goopg's Gather totals 5611.26, which is below its own child Nested
    Loop's 9012.36.
  - The family is the same MECHANISM as Q6 (the open item in the
    M0146-0012a slice C ledger row).

## Notes

- Q32 and Q92 are no longer the SUBPLAN family, whose target M0145-0008y
  is closed. Both SubPlans now match PG's shape except the date\_dim probe.
  At SF1 the upper spine also matches (Hash Join over the Gather).
- **Gather Merge display.** goopg's Gather Merge prints its child's startup
  without the 1000 setup cost, which then shows up on the Finalize node.
  - Example, Q77 sr arm: Partial 3554.39, Gather Merge 3554.39, Finalize
    4553.32.
  - PG: Partial 3517.98, Gather Merge 4517.99.
  - This is a display inconsistency only; it does not change the choice.
