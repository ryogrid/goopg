# M0122-0012 census 2026-10-28d (G1/G2, return-first rule)

Binary: `5a89cc5f5` (after C57–C60: aggregate read sets, in-place early scan quals, the int-lane open-addressing
probe index and group table). It replaces `census-20261028c/` for ranking. Release-PG numbers are the committed values
(`analysis/timing-after-planprity-match/data/*-pgrel.tsv`).

## Method

- TPC-DS SF0.25: goopg median of three `scripts/tpcds-sf025-regression.sh sweep` reports (`sweep-20261028-160851`,
  `-161347`, `-161838`). PG: `pgrel_median_ms`.
- TPC-H SF1: `tools/tpch-gap.sh` three times on `tmp/q17-clone` (pass 2), median. PG: `pgrel_median3_pass2_ms`.
- **Host load 6–10.5 during the runs** (an unrelated validator workload spawning Chrome): absolute times run ~15 %
  above census-20261028c's. The timings are a ranking input, never an A/B of record.

## G2 — combined top 10 by absolute excess (goopg − release PG)

| # | query | goopg ms | PG ms | excess ms | ratio |
|---|---|---|---|---|---|
| 1 | DS Q14 | 8724 | 4790 | 3934 | 1.82 |
| 2 | DS Q23 | 3936 | 1322 | 2614 | 2.98 |
| 3 | DS Q72 | 1752 | 589 | 1163 | 2.97 |
| 4 | H Q13 | 2010 | 910 | 1100 | 2.21 |
| 5 | H Q17 | 1510 | 730 | 780 | 2.07 |
| 6 | DS Q47 | 1214 | 515 | 699 | 2.36 |
| 7 | DS Q78 | 913 | 279 | 634 | 3.27 |
| 8 | DS Q67 | 1438 | 882 | 556 | 1.63 |
| 9 | DS Q39 | 997 | 451 | 546 | 2.21 |
| 10 | DS Q59 | 845 | 355 | 490 | 2.38 |

**Not ranked: DS Q7** (sweep median 1189 ms vs PG 104 ms), the in-sweep server-age artefact recorded in
census-20261028c: warm on a fresh server it runs in 110 ms. H Q18 (C57, C60) and H Q10 (C58) left the top 10; DS Q67
and DS Q59 entered. Next below: DS Q64 454, H Q2 450, H Q12 450 ms.

Full rankings: `tpcds-sf025-g2.tsv`, `tpch-sf1-g2.tsv`.
