# M0122-0012 census 2026-10-28 (G1/G2, return-first rule)

Binary: HEAD `f645129a8` (after C32–C37: lazy probe deform, index-scan read sets, skipped-column runs, skip-scan
look-ahead, inline integer key compares). It replaces `census-20261027b/` for ranking; that census stays as the
before-picture. Release-PG numbers are the same committed values `census-20261027b` used
(`analysis/timing-after-planprity-match/data/*-pgrel.tsv`; they change only on a corpus or query change).

## Method

- TPC-DS SF0.25: goopg median of three `scripts/tpcds-sf025-regression.sh sweep` reports (fresh server per sweep,
  each query once): `sweep-20261028-044329`, `-044435`, `-044536`. PG: `pgrel_median_ms`.
- TPC-H SF1: `tools/tpch-gap.sh` three times on `tmp/q17-clone` (pass 2 of the acceptance runner), median. PG:
  `pgrel_median3_pass2_ms`.
- Host load 1.1–1.6 during the runs; timings are a ranking input, never an A/B of record.

## G2 — combined top 10 by absolute excess (goopg − release PG)

| # | query | goopg ms | PG ms | excess ms | ratio | census-20261027b excess |
|---|---|---|---|---|---|---|
| 1 | DS Q14 | 7995 | 4790 | 3205 | 1.67 | 4322 |
| 2 | DS Q23 | 3953 | 1322 | 2631 | 2.99 | 2396 |
| 3 | H Q18 | 3720 | 2670 | 1050 | 1.39 | 690 |
| 4 | H Q13 | 1860 | 910 | 950 | 2.04 | 1010 |
| 5 | H Q17 | 1630 | 730 | 900 | 2.23 | 1040 |
| 6 | DS Q72 | 1385 | 589 | 796 | 2.35 | 912 |
| 7 | DS Q10 | 721 | 178 | 543 | 4.05 | 553 |
| 8 | DS Q78 | 818 | 279 | 539 | 2.93 | 589 |
| 9 | DS Q39 | 953 | 451 | 502 | 2.11 | 672 |
| 10 | DS Q59 | 817 | 355 | 462 | 2.30 | 499 |

DS Q47 and Q67 left the top 10 (excess 457 and 426 ms). Full per-corpus rankings: `tpcds-sf025-g2.tsv`,
`tpch-sf1-g2.tsv`.

H Q18's rise (690 → 1050 ms) is host variance, not a regression. Its instructions:u fell from 41.912 G at
`a8367e529` (the census-b binary) to 40.760 G at HEAD. Back-to-back wall time: 4.44 / 4.38 s at `a8367e529` against
4.38 / 4.22 s at HEAD.

No query is within 10 % and 50 ms of release PG among the top 10, so M0122-0012 is not EXHAUSTED.
