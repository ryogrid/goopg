# M0122-0012 census 2026-10-28c (G1/G2, return-first rule)

Binary: `c9acfdee9` (after C57 — a seq scan read only by an aggregate decodes only the aggregate's columns). It
replaces `census-20261028b/` for ranking. Release-PG numbers are the committed values
(`analysis/timing-after-planprity-match/data/*-pgrel.tsv`).

## Method

- TPC-DS SF0.25: goopg median of three `scripts/tpcds-sf025-regression.sh sweep` reports (`sweep-20261028-140059`,
  `-140527`, `-140958`). PG: `pgrel_median_ms`.
- TPC-H SF1: `tools/tpch-gap.sh` three times on `tmp/q17-clone` (pass 2), median. PG: `pgrel_median3_pass2_ms`.
- Host load 1.6–5.1 during the runs (rising during the third sweep). The timings are a ranking input, never an A/B of
  record.

## G2 — combined top 10 by absolute excess (goopg − release PG)

| # | query | goopg ms | PG ms | excess ms | ratio |
|---|---|---|---|---|---|
| 1 | DS Q14 | 8199 | 4790 | 3409 | 1.71 |
| 2 | DS Q23 | 3431 | 1322 | 2109 | 2.60 |
| 3 | DS Q72 | 1515 | 589 | 926 | 2.57 |
| 4 | H Q13 | 1820 | 910 | 910 | 2.00 |
| 5 | H Q17 | 1430 | 730 | 700 | 1.96 |
| 6 | H Q18 | 3200 | 2670 | 530 | 1.20 |
| 7 | DS Q78 | 759 | 279 | 480 | 2.72 |
| 8 | DS Q39 | 922 | 451 | 471 | 2.04 |
| 9 | DS Q47 | 972 | 515 | 457 | 1.89 |
| 10 | H Q10 | 790 | 350 | 440 | 2.26 |

**Not ranked: DS Q7.** Its sweep median was 1016 ms (runs 1108, 288, 1016; earlier censuses 228 and 320 ms) against PG's
104 ms. Warm on a fresh server (`tools/ds-warm.sh`) it runs in 110 ms, and its instructions are flat across C57
(5.393 vs 5.394 G). The sweep figure is the in-sweep server-age effect (it runs after heavier queries), not a property
of Q7. DS Q59 (411 ms) left the top 10; H Q18 fell from 1000 to 530 ms after C57.

Full rankings: `tpcds-sf025-g2.tsv`, `tpch-sf1-g2.tsv`.
