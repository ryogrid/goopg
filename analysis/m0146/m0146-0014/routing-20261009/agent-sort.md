# M0146-0014 routing 2026-10-09: SORT / Incremental Sort family

This is a plan-text analysis of `goopg-sf{025,1}.plans.txt` against
`pg-sf{025,1}.plans.txt`. The previous target, M0146-0006, is closed `[x]`.
Q87 at SF1 is a MATCH, so it has no row.

| Query | Scale | First divergence (short) | Cause with cost numbers | Family | Target |
|---|---|---|---|---|---|
| Q4 | SF0.25 | Limit: PG Incremental Sort, goopg Sort | Join-order tie. PG's chain sits over a Merge Join on `customer_id` (presorted, hence the Incremental Sort) and costs 188166.22. goopg's unordered NL chain costs 187886.17, 0.15% cheaper, so it gets a full Sort. M0146-0006's own closure already called this a tie. | COSTTIE | M0146-0014 (waived residual) |
| Q4 | SF1 | `year_total` CTE arm: PG HashAggregate, goopg GroupAggregate | PG's serial HashAggregate is 188847.71..221654.86, input width 213. goopg elects GroupAgg←Gather Merge←Sort at 548317.81, sort width 1206 (customer 726 vs 191), so its hashed arm costs more than 548k (>2.5× PG). The spill tail is priced in `EntryBytes` full-row width, not `input_width`. | WIDTH | M0141-S2a-fix2r-a (filed, open) |
| Q6 | both | under GroupAggregate: PG Nested Loop (item probe above Gather Merge), goopg Sort | PG files `i_current_price > 1.2*(SubPlan 2)` as item's base restriction: probe 0.29..1513.82, item stays above the Gather Merge, NL 427656.80. goopg filters at the Gather and never charges the SubPlan (≈94×1471.50 missing): Gather 18921.80. At SF1: 1696053 vs 72056. | MECHANISM | NEW: distribute_qual_to_rels for a single-relation correlated SubPlan qual on a non-leading binding, plus set_rel_consider_parallel keeping that rel above the Gather (ledger 2026-10-05 M0145-0008y residual 1) |
| Q35 | both | under GroupAggregate: PG Incremental Sort (Presorted `ca_state`), goopg Sort | Decided by the LIMIT fraction. PG drives a NL from Gather Merge→Sort(`ca`), with a Materialize inner. Its total, 54.67M, is above its own ca-probe alternative (≈53.32M), but under LIMIT 100 it costs 4.09M against goopg's 58.33M (SF1: 26.9M vs 781.2M). goopg has no ca_state-ordered candidate at the grouping arm. | MECHANISM | NEW: keep the group-key-ordered NL (generate_useful_gather_paths outer + matpath) through add_path and elect its make_ordered_path Incremental Sort through get_cheapest_fractional_path (ledger 2026-10-04 M0146-0005ea item 2, unrouted) |
| Q38 | both | under Gather Merge: PG worker Unique, goopg Sort | Partial distinct (`Unique→GM→Unique→Sort`) against plain GM→Sort. PG's worker Unique removes no rows (371→371; SF1 1149→1149) and adds only +2.78 / +8.62 on arms of 9031.81 / 25716.81 (≈0.03%). PG itself takes it inconsistently: not on the store arm, and nowhere in Q87 SF1. | COSTTIE | M0146-0014 (waived residual) |
| Q87 | SF0.25 | under Gather Merge: PG worker Unique, goopg Sort | Same partial-distinct tie as Q38. Web arm: PG worker Unique 7922.38 over Sort 7919.60, rows unchanged at 371, top Unique 9031.81. goopg Sort 7868.99 → Unique 8983.18. SF1 is a MATCH (PG drops the worker Unique there), which confirms the tie. | COSTTIE | M0146-0014 (waived residual) |
| Q49 | both | Limit: PG Incremental Sort, goopg Sort | PG's top Unique (the UNION dedup) keeps its Sort's pathkeys, giving presorted key `('web'::text)` → Incremental Sort 10459.26..10459.41. PG takes it by make_ordered_path's rule, not by cost. goopg's `inputNodePathkeys` has no Distinct/DistinctOn arm, so presorted=0 and it uses a full Sort (13536.61). The 10459 vs 13536 gap is B8 bitmap probes (12.39 vs 7.94). | MECHANISM | NEW: create_upper_unique_path keeps subpath pathkeys — add a Unique arm to `inputNodePathkeys` so create_ordered_paths sees presorted keys over a UNION |
| Q64 | both | CTE GroupAggregate: PG Incremental Sort (Presorted `item.i_item_sk`), goopg Sort | The NL chain is ordered by the outer GroupAggregate on `catalog_sales.cs_item_sk`. PG's equivalence class (cs_item_sk = ss_item_sk = i_item_sk) satisfies the group key `i_item_sk`: Incremental Sort 13874.09 over NL 13874.00 (SF1 49259.13). goopg's pathkeys are syntactic (`pathKeyEqual`/exprEqual), so there is no match and it uses a full Sort (13839.49; SF1 53270.46). | MECHANISM | NEW: EC-canonical pathkeys — pathkeys_count_contained_in over EquivalenceClasses (make_pathkeys_for_sortclauses / convert_subquery_pathkeys) |
| Q72 | SF0.25 | under GroupAggregate: PG NL Left (presorted via GM→Sort), goopg Sort | PG sorts 1 row per worker and reaches GroupAgg through Gather Merge (+1000.16). goopg puts a plain Gather lower (+1000.10) and sorts 1 row at the leader (+0.01). Both shapes price within ≈0.01 (<0.001%). goopg's 15011.52 vs PG's 15490.82 comes from the inventory probe (87.62 vs 101.89; rows 527 vs 176, M0146-0009p). | COSTTIE | M0146-0014 (waived residual) |
| Q72 | SF1 | under GroupAggregate: PG NL Left, goopg Sort | goopg hash-joins `d3` (Seq Scan 2252.49, +3229 over Gather 61108.13) and estimates 548 rows, against PG's 5×0.31 index probe and 2 rows. Even at goopg's B8 bitmap price (12.27, see SF0.25) the NL would cost ≈61. The parameterised d3 candidate looks missing at SF1; the Sort follows from that. | OTHER | NEW: Q72 SF1 — parameterised `d3` probe above the 1-worker Gather not elected and join rows 548 vs 2 (needs an addPath trace at the d3 joinrel) |
| Q95 | SF0.25 | under Aggregate: PG NL Semi (outer leaf GM→Sort), goopg Sort | PG's parallel leaf (ws1⋈web_site⋈Memoize date_dim) is GM 7713.16 → NL 7721.28. goopg runs it serially at 8450.52 (+9.4%), and its estimated partial (~7.7k) would win. At SF1 goopg does elect the Gather (26104.52), so the candidate is lost only at SF0.25 (2 workers). | OTHER | NEW: Q95 SF0.25 — partial leaf under the semijoin chain not elected at 2 workers (needs a generate_useful_gather_paths / DPPATH gather trace) |
| Q95 | SF1 | NL→Hash Join qual-placement | Same shape. PG's parameterised semijoin RHS (hash ws_wh_1=wr, inner IOS probe wr=ws1) also keeps `Join Filter: ws1.ws_order_number = ws_wh_1.ws_order_number`; goopg drops it (hash join 167485.02 vs PG 169714.13). The SF0.25 plans show the same omission. Not cost-driven. | MECHANISM | NEW: get_joinrel_parampathinfo / generate_join_implied_equalities — re-derive the EC outer clause as a Join Filter on a parameterised semijoin RHS |

Family counts, one per (query, scale) record. There are 17 records; Q87 SF1
is a MATCH and is not counted.

| Family | Records | Which |
|---|---|---|
| MECHANISM | 9 | Q6 ×2, Q35 ×2, Q49 ×2, Q64 ×2, Q95 SF1 |
| COSTTIE | 5 | Q4 SF0.25, Q38 ×2, Q87 SF0.25, Q72 SF0.25 |
| OTHER | 2 | Q72 SF1, Q95 SF0.25 |
| WIDTH | 1 | Q4 SF1 |
| B8, B15, RELPAGES | 0 | — |

B8 is a secondary contributor in Q49 and the Q72 `d3` probe, but it does not
decide either sort choice. RELPAGES adds about 8–9% to the SF1 seq-scan terms
in Q72 and Q95, but it never decides the shape.
