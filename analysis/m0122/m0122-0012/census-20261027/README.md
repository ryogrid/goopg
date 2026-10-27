# M0122-0012 census 2026-10-27 (G1/G2, return-first rule)

Binary: HEAD `102bc10a4` (TPC-DS sweeps also include `da7f0c117` and the M0122-0019 working tree, all
plan- and executor-equivalent for timing: those commits change only EXPLAIN accounting).
Release-PG numbers: `analysis/timing-after-planprity-match/data/*-pgrel.tsv` (non-cassert PG 18.3, reused —
they change only on a corpus or query change).

## Method

- TPC-DS SF0.25: goopg median of three `scripts/tpcds-sf025-regression.sh sweep` reports (fresh server per
  sweep, each query once — the harness the release-PG timing mirrors): `sweep-20261027-115232`, `-122456`,
  `-124929`. PG: `pgrel_median_ms`.
- TPC-H SF1: `tools/tpch-gap.sh` three times on `tmp/q17-clone` (pass 2 of the acceptance runner), median. PG:
  `pgrel_median3_pass2_ms`.
- Split (`tools/g1-split.sh`): planning = min of two plain `EXPLAIN (SUMMARY ON)` Planning Time (first statement,
  M0122-0019 makes it the planner's duration); allocation = Go-heap TotalAlloc / Mallocs delta per run.
- Release-PG planning for the planning outliers: `EXPLAIN (SUMMARY ON)` on the private non-cassert cluster
  (`bench/pg-release/data-tpcds`, :5556), min of three: Q64 51.9 ms, Q72 16.5 ms, Q14 2.4 ms.

## G2 — combined top 10 by absolute excess (goopg − release PG)

| # | query | goopg ms | PG ms | excess ms | ratio | goopg planning ms | MB/run |
|---|---|---|---|---|---|---|---|
| 1 | DS Q14 | 10594 | 4790 | 5804 | 2.21 | 17.4 | 3874 |
| 2 | DS Q23 | 3674 | 1322 | 2352 | 2.78 | 5.6 | 3410 |
| 3 | DS Q72 | 2564 | 589 | 1975 | 4.35 | 73.0 (PG 16.5) | 78 |
| 4 | H Q17 | 2190 | 730 | 1460 | 3.00 | 0.7 | 299 |
| 5 | DS Q67 | 2226 | 882 | 1344 | 2.52 | 4.7 | 1270 |
| 6 | H Q18 | 3910 | 2670 | 1240 | 1.46 | 2.9 | 2229 |
| 7 | H Q13 | 2140 | 910 | 1230 | 2.35 | 0.7 | 267 |
| 8 | DS Q39 | 1350 | 451 | 899 | 2.99 | 2.5 | 1147 |
| 9 | DS Q64 | 1019 | 173 | 846 | 5.89 | **636.1 (PG 51.9)** | 348 |
| 10 | DS Q78 | 1037 | 279 | 758 | 3.72 | 7.9 | 880 |

Full per-corpus rankings: `tpcds-sf025-g2.tsv`, `tpch-sf1-g2.tsv`; split: `*-split.txt`.

## Finding

Q64's excess is mostly planning: 636 ms against release PG's 52 ms, i.e. ~584 ms of its 846 ms excess and ~57 % of
its whole goopg time. Every other top-10 query plans in under 20 ms except Q72 (73 vs 16.5 ms, ~2 % of Q72). This
is a candidate no seeded pool entry covers (C15 names pathkey canonicalisation and sublink lowering, not the
join search); it enters the pool as C16 and ranks first by predicted gain.
