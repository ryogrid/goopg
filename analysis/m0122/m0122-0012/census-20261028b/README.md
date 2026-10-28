# M0122-0012 census 2026-10-28b (G1/G2, return-first rule)

Binary: HEAD `6a2f5b0e1`, after C45–C49 (bare-column probe key, aggregate finalize by pointer, in-place heap fetch,
visibility by pointer). It replaces `census-20261028/` for ranking. Release-PG numbers are the same committed values
(`analysis/timing-after-planprity-match/data/*-pgrel.tsv`).

## Method

- TPC-DS SF0.25: goopg median of three `scripts/tpcds-sf025-regression.sh sweep` reports (`sweep-20261028-101220`,
  `-101645`, `-102105`). PG: `pgrel_median_ms`.
- TPC-H SF1: `tools/tpch-gap.sh` three times on `tmp/q17-clone` (pass 2), median. PG: `pgrel_median3_pass2_ms`.
- Host load 0.9–2.0 during the runs. The timings are a ranking input, never an A/B of record.

## G2 — combined top 10 by absolute excess (goopg − release PG)

| # | query | goopg ms | PG ms | excess ms | ratio | census-20261028 excess |
|---|---|---|---|---|---|---|
| 1 | DS Q14 | 8166 | 4790 | 3376 | 1.70 | 3205 |
| 2 | DS Q23 | 3183 | 1322 | 1861 | 2.41 | 2631 |
| 3 | H Q18 | 3670 | 2670 | 1000 | 1.37 | 1050 |
| 4 | H Q13 | 1800 | 910 | 890 | 1.98 | 950 |
| 5 | DS Q72 | 1438 | 589 | 849 | 2.44 | 796 |
| 6 | H Q17 | 1450 | 730 | 720 | 1.99 | 900 |
| 7 | DS Q78 | 773 | 279 | 494 | 2.77 | 539 |
| 8 | DS Q39 | 907 | 451 | 456 | 2.01 | 502 |
| 9 | DS Q47 | 964 | 515 | 449 | 1.87 | — |
| 10 | DS Q59 | 796 | 355 | 441 | 2.24 | 462 |

DS Q10 left the top 10 (C38, C48, C49). DS Q47 entered. H Q10 and H Q1 follow at 420 ms. Full per-corpus rankings
are in `tpcds-sf025-g2.tsv` and `tpch-sf1-g2.tsv`. No top-10 query is within 10 % and 50 ms of release PG, so
M0122-0012 is not EXHAUSTED.
