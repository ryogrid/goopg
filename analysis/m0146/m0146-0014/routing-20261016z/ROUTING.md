# M0146-0014 — S9 pass at c2a4421fc (2026-10-16): files M0146-0219

Trigger: every item 0-9 task is gated:
- M0146-0214 is blocked-on M0146-0216;
- M0146-0215 and M0146-0218 wait on the owner's stats import;
- M0146-0216 waits on the owner's index refresh;
- the rest are channels or below item 10.

c2a4421fc changed `internal/optimizer`, so the census is fresh. The shipped-corpus captures are M0146-0217's
`analysis/m0146/m0146-0217/census/m17-*`. They were built from the tree that became c2a4421fc; that commit adds
only a test file on top of them. The first-divergence files here were computed from those captures.

| corpus | capture | match |
|---|---|---|
| TPC-H | `m17-tpch*` | 22 / 22 |
| TPC-DS SF0.25 | `m17-tpcds-sf025*` | 86 / 99 |
| TPC-DS SF1 | `m17-tpcds-sf1*` | 73 / 99 |
| TPC-DS SF1, PG statistics imported (private clone) | `imp-sf1.plans.txt` | **85 / 99** |

## The imported-statistics SF1 census (new)

A private clone of the SF1 corpus on port 5589 ran the HEAD binary. All 478 `pg_restore_*_stats` calls from
`pg_dump --statistics` of the reference `tpcds` returned true. The clone was then captured with
`capture-tpcds.sh`. PG arm: `m17-tpcds-sf1-pg.plans.txt`.

- With PG's statistics, SF1 goes from 73 to 85 / 99.
- These SF1 records disappear, so they are ANALYZE sample variance: join-order Q4, Q11, Q31, Q62, Q99;
  sort-strategy Q18, Q38, Q56, Q87; parallelism Q7; qual-placement Q69; parameterisation Q76.
  Their route is the live M0146-0215 / M0146-0218 import, so they are not actionable as planner work.
  M0146-0215's SF0.25 import census showed the same for Q99 and for Q77's aggregation record (Q77 then first
  diverges deeper, at a sort-strategy node).
- These records remain, so they are planner logic: aggregation-strategy Q13, Q23, Q26, Q48, Q71; join-order Q64
  (M0146-0214), Q77, Q78; parallelism Q16, Q39; sort-strategy Q52 (M0146-0216), Q72; scan-type Q53, Q63.

## Ranking (actionable first divergences, both TPC-DS corpora; live-task and import-routed records excluded)

| category | actionable records |
|---|---|
| aggregation-strategy | 6 (SF0.25 Q61; SF1 Q13, Q23, Q26, Q48, Q71) |
| parallelism | 4 (SF0.25 Q16, Q39; SF1 Q16, Q39) |
| sort-strategy | 3 (SF0.25 Q4, Q72; SF1 Q72) |
| join-order | 2 (SF1 Q77, Q78) |
| scan-type | 2 (SF1 Q53, Q63) |
| qual-placement, join-method | 1 each (SF0.25 Q78, Q69) |

On the raw shipped-corpus counts, join-order leads with 8. Five of those eight are import-routed, so with the
exclusion above aggregation-strategy leads.

## Filed

**M0146-0219**: TPC-DS SF0.25 Q61, aggregation-strategy, SHAPE-DIFF at depth 3. PG plans each scalar FROM-subquery
aggregate as `Finalize Aggregate → Gather → Partial Aggregate`; goopg plans `Aggregate → Gather`. SF1 Q13 and Q48
show the same pattern at depth 0.
