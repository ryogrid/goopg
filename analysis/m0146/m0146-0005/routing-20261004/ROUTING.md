# M0146-0005 slice 115 — first-divergence routing refresh, TPC-DS SF0.25 + SF1

Capture: the M0146-0009m fire-set candidate at HEAD `af2c28f5f`
(`goopg-<sf>.plans.txt`, `pg-<sf>.plans.txt`, `diff-<sf>.txt`; SF0.25 match
39/99, SF1 30/99). Plan-text analysis by five read-only passes over the
brief in this directory's commit (`tmp/route-brief.md`, families as in
slice 114).

Scope:

- **SF0.25:** the 16 records whose 2026-10-02 targets (M0146-0005dg..dq,
  M0146-0009i) have since closed are re-routed. Q41 and Q75 now match.
  The other 2026-10-02 rows keep their open targets (carried, below).
- **SF1:** never routed before.
  - The 18 queries that diverge only at SF1 are routed.
  - 8 queries that diverge at both scales, but whose SF1 categories differ
    materially from SF0.25, were re-checked at SF1.
  - The remaining shared queries carry their SF0.25 route. Their SF1
    category sets differ by at most one category.
- Q36, Q70 and Q86 are capture errors at both scales (syntax error in the
  capture script), not plan records.

## Re-routed SF0.25 records (targets closed since 2026-10-02)

| Query | First divergence now | Family | Target |
|---|---|---|---|
| Q1 | inner probe of `customer`: Bitmap Heap Scan vs PG Index Scan on customer\_pkey; everything above matches | B8 | M0145-0008ag (owner-parked multiplier) |
| Q2 | CTE `wswscs`: serial HashAgg/Hash Join/Append vs PG Finalize←Gather←Partial HashAgg←Parallel Append. goopg's Hash Join totals less than its own Append input, and its Append charges 0.01/row (PG `cost_append`: cpu\_tuple\_cost × 0.5) | PARTIAL (+ Append pricing anomaly) | M0146-0027; anomaly ledgered |
| Q10, Q35 | an uncorrelated restriction holding an OR of hashed SubPlans stays above the search; PG makes it a baserestrictinfo of `c` and costs the AlternativeSubPlan at the scan | NEW | M0146-0005dx |
| Q14 | per arm: cross\_items → store\_sales by Bitmap Heap Scan vs PG cross\_items → item → store\_sales Index Scan; the CTE estimate now matches (0009i) | B8 | M0145-0008ag |
| Q23 | CTE `best_ss_customer`: Seq Scan + Hash of `customer` vs PG's clause-less Index Only Scan on customer\_pkey | IOS | M0146-0019a |
| Q44 | Merge Join on `rnk` over two WindowAgg outputs with no Sort: goopg claims the window column's order, PG sorts each side (values safe: goopg's merge executor sorts its inputs itself) | NEW | M0146-0005dw |
| Q47, Q57 | the top Merge Join's inner is a Merge Join fed bare; PG puts a Materialize on it (`final_cost_mergejoin` materialize\_inner, no mark/restore). Window order now matches | NEW | M0146-0005dv |
| Q49 | top Sort vs PG Incremental Sort; most of the cost gap is the bitmap probe on \*\_sales\_pkey (B8) | SORT | M0146-0006 (then M0145-0008ag) |
| Q59 | final join: Hash Join (rows=15) vs PG NL + Materialize (rows=1); the same joinrel is estimated 15 vs 1. The `-52` EC clause is now derived | STATS | M0146-0009 |
| Q67 | MixedAggregate (rows clamped to input, 3140) vs PG sorted rollup GroupAggregate (18531) | GSETS (+ STATS) | M0146-0020b; M0146-0009o |
| Q69 | the store\_sales semi join: goopg Parallel Hash Semi Join vs PG unique-ified (ss ⋈ date\_dim) HashAgg + customer\_pkey probe | NEW | M0146-0005dy |
| Q72 | the partial subtree stops below the warehouse join; PG's reaches the item join under Gather Merge + per-worker Sort. Also the inventory\_pkey probe rows 527 = 3 × PG's 176 | PARTIAL (+ STATS) | M0146-0027; M0146-0009p |
| Q83 | web\_returns arm: the Memoize'd date\_dim\_pkey probe is the whole excess (0.61 vs 0.50 per probe) | B8 | M0145-0008ag |
| Q95 | goopg sorts above the NL Semi Join chain; PG presorts via Gather Merge + Sort at the outer leaf. The NL-semi shape now matches (0005dq) | PARTIAL (+ IOS lower) | M0146-0027; M0146-0019a |

## SF1-only records (match at SF0.25)

| Query | First divergence | Family | Target |
|---|---|---|---|
| Q3, Q7, Q22, Q24, Q53, Q55, Q56, Q63, Q75, Q82, Q84, Q89 | a parallel shape PG builds from a partial `item` (or `customer_address`) path is missing: goopg's SF1 relations are below the 1024-page `min_parallel_table_scan_size` (item 736 vs PG 1284; customer\_address 955 vs 1136) | RELPAGES (SF1 load) | owner reload of the SF1 cluster (M0146-0009h); see below |
| Q13, Q48 | plain Aggregate vs PG Finalize/Partial Aggregate: < 0.1 cost units apart (PG itself flips between scales) | COSTTIE | M0146-0014 |
| Q18 | MixedAggregate vs PG sorted rollup; goopg's grouping-sets rows are about ¼ of PG's (49 vs 213) | GSETS (+ STATS) | M0146-0020b; M0146-0009o |
| Q31, Q62 | dimension join order; both orders within 0.04% / 0.1% | COSTTIE | M0146-0014 |
| Q80 | csr/wsr arms: within 0.4%, tipped by relpages drift (item, date\_dim) | COSTTIE (RELPAGES) | M0146-0014; SF1 reload |

## Shared records re-checked at SF1

| Query | SF1 first divergence | Same family as SF0.25? | Target |
|---|---|---|---|
| Q4, Q11 | CTE `year_total` arms: GroupAgg←Gather Merge←Sort vs PG serial HashAggregate. Group counts agree; goopg's hashed arm is priced over 2.5× PG's, because its spill tail uses `hashsize.EntryBytes` full-row width (Sort width 1206 vs PG input width 213) | no (SF0.25: COSTTIE/SORT) | M0141-S2a-fix2r-a (new) |
| Q26 | top aggregation shape, overhead tie (1017.34 vs 1017.00); the total gap is the catalog\_sales scan (relpages) | yes (COSTTIE) | M0146-0014 |
| Q37 | missing parallel boundary (item relpages) + 2× inventory\_pkey probe | yes (RELPAGES + B8) | SF1 reload; M0145-0008ag |
| Q40 | GroupAgg vs PG Finalize/Partial GroupAgg | yes (PARTIAL) | M0146-0027 |
| Q61 | join order inside the Gather; tipped by relpages (scan deltas exceed the gap) | no → RELPAGES | SF1 reload |
| Q71 | GroupAgg←Sort←Gather vs PG's partial split; the split arm loses (ledgered by M0146-0009n). SUBQSCAN below | no → PARTIAL | M0146-0027 (then M0146-0026) |
| Q78 | the top Merge Left Join keeps `ss_sold_year = *_sold_year` as a merge key; PG's `reconsider_outer_join_clauses` replaces the redundant outer-join clause with a constant-TRUE dummy. Also the store\_returns anti join estimates 30 vs 281 per worker | no → NEW | M0146-0005dz; M0146-0009 |

## Carried from 2026-10-02 (target still open or held)

B8 → M0145-0008ag: Q17, Q25, Q29, Q30, Q34, Q46, Q60, Q68, Q73, Q76, Q81,
Q85 · COSTTIE → M0146-0014: Q5, Q26, Q45, Q33, Q39, Q58, Q61 (SF0.25),
Q65, Q77, Q99 · SUBPLAN → M0145-0008y \[!\]: Q6, Q32, Q92 · PARTIAL →
M0146-0027: Q19, Q38, Q87 · SORT → M0146-0006: Q64 (Q78 at SF0.25) ·
SUBQSCAN → M0146-0026: Q71 (SF0.25) · PARAM-APPEND → M0146-0005dp \[!\]:
Q54 · B15 → ledger take3-B-15-blocked-2: Q16 · RELPAGES/B15/B8: Q37,
Q42, Q52 · PAR-APPEND → M0140-0006a-c: Q66 · HASHSPILL → M0139-0007a:
Q79 · rendering → M0146-0042: Q39's Sort Key alias.

## The SF1 cluster's page counts

The SF1 goopg bench cluster's relation sizes, derived from seq-scan costs,
are not PG's in either direction:

| table | goopg | PG | goopg vs PG |
|---|---|---|---|
| item | 736 | 1284 | −43% |
| customer | 2046 | 2872 | −29% |
| customer\_address | 955 | 1136 | −16% |
| date\_dim | 1522 | 1424 | +7% |
| web\_sales | ≈ 20552 | ≈ 18824 | +9% |
| catalog\_sales | ≈ 41066 | ≈ 37448 | +10% |
| store\_sales | 59540 | 51784 | +15% |

- A fresh goopg load of the same files gives PG's size exactly (M0146-0009h:
  item 1284, store\_sales 12936 at SF0.25).
- So the SF1 cluster predates the current on-disk format: the char-heavy
  dimensions shrank and the numeric-heavy facts grew, consistent with the
  "stale char(n) data" of the 2026-09-26 triage.
- About 15 SF1 records hinge on it. The owner reload should be checked with
  `pg_class.relpages` before these records are re-traced.

## Checked, not a defect

- **Q44's merge over an unsorted window output** cannot return wrong
  results. goopg's merge-join executor sorts each input by its merge key
  itself (`mergeSortedSource`, join\_merge\_stream.go). A probe with
  `rank() OVER (PARTITION BY p ORDER BY y)` on both sides returns PG's
  count and sum.
- **Q78's top Incremental Sort** claims presorted key `ss_customer_sk` over
  that merge. The merge emits in merge-key order (customer\_sk first), so
  the claim holds. The SF0.25 checksum matches the oracle.

## Anomalies (ledgered, not routed as records)

- Q2: a serial Hash Join totals less than its own Append input, and the
  Append charges 0.01/row instead of PG's `cpu_tuple_cost × 0.5`.
- Q14: a Parallel Append totals less than its first child (15411.62 vs
  17923.20). The Q14b top Nested Loop is cheaper than its GroupAggregate
  inputs (the date\_dim InitPlan is not charged).
- Q26 (SF1), Q57, Q65, Q67: a Sort reports more rows than its input.
- Q62/Q80 (SF1): Gather Merge startup omits `parallel_setup_cost`, which
  appears on the Finalize above it instead.
- Q23: a Hash Right Semi Join on the grouped CTE outputs half its input
  (eqjoinsel\_semi default) where PG keeps all of it.
