# M0122-0012 census 2026-10-27b (G1/G2, return-first rule)

Binary: HEAD `a8367e529` (after slices 104–109: C23, C25, C26, C27, C28, C29). It replaces `census-20261027/` for
ranking; that census is kept as the before-picture. Release-PG numbers are the same committed
`analysis/timing-after-planprity-match/data/*-pgrel.tsv` (they change only on a corpus or query change).

## Method

- TPC-DS SF0.25: goopg median of three `scripts/tpcds-sf025-regression.sh sweep` reports (fresh server per sweep,
  each query once): `sweep-20261027-225659`, `-225809`, `-225915`. PG: `pgrel_median_ms`.
- TPC-H SF1: `tools/tpch-gap.sh` three times on `tmp/q17-clone` (pass 2 of the acceptance runner), median. PG:
  `pgrel_median3_pass2_ms`.
- Host load 1.8–2.3 during the runs; timings are a ranking input, never an A/B of record.

## G2 — combined top 10 by absolute excess (goopg − release PG)

| # | query | goopg ms | PG ms | excess ms | ratio | census-20261027 excess |
|---|---|---|---|---|---|---|
| 1 | DS Q14 | 9112 | 4790 | 4322 | 1.90 | 5804 |
| 2 | DS Q23 | 3718 | 1322 | 2396 | 2.81 | 2352 |
| 3 | H Q17 | 1770 | 730 | 1040 | 2.42 | 1460 |
| 4 | H Q13 | 1920 | 910 | 1010 | 2.11 | 1230 |
| 5 | DS Q72 | 1501 | 589 | 912 | 2.55 | 1975 |
| 6 | H Q18 | 3360 | 2670 | 690 | 1.26 | 1240 |
| 7 | DS Q39 | 1123 | 451 | 672 | 2.49 | 899 |
| 8 | DS Q78 | 868 | 279 | 589 | 3.11 | 758 |
| 9 | DS Q47 | 1092 | 515 | 577 | 2.12 | — |
| 10 | DS Q67 | 1452 | 882 | 570 | 1.65 | 1344 |

DS Q64 (planning-dominated in the first census) left the top 10. Full per-corpus rankings: `tpcds-sf025-g2.tsv`,
`tpch-sf1-g2.tsv`.
