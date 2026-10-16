# M0146-0014 — S9 pass at 9fcfbc795 (2026-10-16): files M0146-0220

Trigger: every item 0-9 task is gated:
- M0146-0214 is blocked-on M0146-0216;
- M0146-0215 and M0146-0218 wait on the owner's stats import;
- M0146-0216 waits on the owner's index refresh.

The shipped-corpus census is M0146-0219's `analysis/m0146/m0146-0219/census/m19-*`, built from 9fcfbc795's code;
that commit adds only a test on top. The `first-divergence-{sf025,sf1,tpch}.txt` files here are computed from it.

| corpus | capture | match |
|---|---|---|
| TPC-H | `m19-tpch*` | 22 / 22 |
| TPC-DS SF0.25 | `m19-tpcds-sf025*` | 88 / 99 |
| TPC-DS SF1 | `m19-tpcds-sf1*` | 74 / 99 |
| TPC-DS SF1, PG statistics imported (private clone, this pass) | `imp-sf1.plans.txt` | **86 / 99** (85 before 9fcfbc795) |

## Exclusions

- Live tasks: Q64 (M0146-0214), Q76 (M0146-0215), SF1 Q52 (M0146-0216), SF1 Q87 (M0146-0218).
- Import-routed: records that disappear under PG's statistics. At SF1, from routing-20261016z's import census and
  this pass's (which agree): Q4, Q7, Q11, Q18, Q31, Q38, Q56, Q62, Q69, Q76, Q87, Q99. At SF0.25 (M0146-0215 import
  census): Q99, and Q77's aggregation record.
- SF0.25 Q38 and Q87 are routed to the import by M0146-0217: Q38 is shown to match under PG statistics; Q87 has the
  same shape.

## Ranking (actionable first divergences, both TPC-DS corpora)

| category | actionable records |
|---|---|
| aggregation-strategy | 4 (SF1 Q23, Q26, Q48, Q71; all four persist under PG statistics) |
| sort-strategy | 3 (SF0.25 Q4, Q72; SF1 Q72) |
| parallelism | 2 (Q39 at both scales) |
| join-order | 2 (SF1 Q77, Q78) |
| scan-type | 2 (SF1 Q53, Q63) |
| qual-placement | 2 (SF0.25 Q78; SF1 Q13, which first diverges at join-method under PG statistics) |
| join-method | 1 (SF0.25 Q69) |

## Filed

**M0146-0220**: TPC-DS SF1 Q48, aggregation-strategy, SHAPE-DIFF at depth 0. PG plans
`Finalize Aggregate → Gather → Partial Aggregate`; goopg plans `Aggregate → Gather`, which is M0146-0219's shape.
Under PG statistics:

| plan | cost |
|---|---|
| goopg no-split arm | 84021.17..84021.19 |
| goopg's Gather (priced at compute_gather_rows = 4) | 84021.16 |
| PG Finalize | 84021.10..84021.11 |

Had PG's scan/join-rel Gather survived, PG's own plain arm would cost about 84020.8 and win. So PG rejects that
Gather against a serial candidate that goopg's `scanJoinRelKeepsGather` does not see, or the check does not run on
this input.
