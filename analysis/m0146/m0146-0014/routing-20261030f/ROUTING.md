# M0146-0014 — census at 5657546d8 (2026-10-30): exhausted
tree=9cff2a6428376dd9655629df15c29bdb30599db2

Trigger: `scripts/s9-census-due.sh` returned DUE (base 10df544e6844, reason=plan-relevant-change). `5657546d8`
touched internal/catalog/pubsub.go, internal/postmaster/dispatch{,_extended}.go and the executor for the
publisher-side subscription slot lifecycle (M0122-0014). This is an unchanged census under S9(4a), so only
this file is committed; the captures were deleted from `tmp/`.

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-H | 22 / 22 | all 0 |
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |

`scripts/s9-census-due.sh --compare tmp/s9w20-census` (exit 0):

- tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt
- tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt
- tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt

Engine binary (AGENT.md G3) and capture repo head, per corpus:

- tpch: tmp/goopg-acceptance-bin sha256 1631de084d8b930a12c246b60b1cb7d499d5d1e40c2ea5cf194ac3fa2cc512ad (the
  served binary the audit arm verified); repo head 5657546d8.
- tpcds-sf025 and tpcds-sf1: tmp/goopg-jointree-bin sha256
  1631de084d8b930a12c246b60b1cb7d499d5d1e40c2ea5cf194ac3fa2cc512ad; repo head 5657546d8.
