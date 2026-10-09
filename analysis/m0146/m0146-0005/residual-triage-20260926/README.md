# M0146-0005 residual triage (2026-09-26, HEAD b57acc6cd)

`join-records.txt` lists every remaining `join-method` / `join-order`
first-divergence record at SF0.25 and SF1 after slices 1–14. Each was
checked against PG's plan (and, where noted, goopg's DPPATH) to find its
cause. Most are no longer join-search gaps.

| records | cause | owner / blocker |
|---|---|---|
| Q79, Q55, Q23, Q30 | goopg's inputs are cheaper than PG's because the TPC-DS clusters hold unpadded `char(n)` (item 896 vs PG 1464; customer 2979 vs 3872; date_dim 2109 vs 2154), and the calibrated `indexProbeCostMultiplier` = 2.0 doubles probe costs. Q55: `item` is ~5.6 MB in goopg, under `min_parallel_table_scan_size`, so no parallel scan and no item-driven partial nested loop (`q55-dppath.txt`: no partial nested loop with outer {2}) | owner reload of the stale TPC-DS clusters (escalated); multiplier owner-parked (M0142-0005c) |
| Q1, Q92 (Q30 partly) | goopg decorrelates a correlated `avg` subquery into a grouped-aggregate join; PG keeps it as a SubPlan in the join filter or scan filter | sublink / unnest pipeline (M0145 family, D1-sublink) |
| Q65 | PG aggregates sorted (GroupAggregate over Gather Merge), and its merge join follows from that ordering; goopg hash-aggregates | M0146-0003 (row-emitting PartialAgg / Finalize chain) |
| Q4, Q11 | the second `year_total` branch: PG's two join orders cost 15677.83 and 15677.85, a tie PG breaks by path order; goopg's version of the tie is shifted by stale heap sizes | none (tie); re-check after the reload |
| Q38, Q87 | SetOp vs HashSetOp (set-operation strategy) | set-op planning (not join search) |
| Q2, Q31, Q97 | CTE / append structure above the joins | CTE / upper-rel planning |
| Q14 (depth 7), Q8 | Q14 now diverges deep inside one branch after slices 8 and 10; Q8's nested loop into `store` has a substr() join filter | not yet traced |

Conclusion: M0146-0005's join-search mechanisms (slices 1–14) have
consumed the records whose cause was join search. The two actionable
blockers are the owner's TPC-DS reload and the multiplier decision.
Q8 and Q14 remain for a later slice.
