# M0146-0014 — census at e3598f114 (2026-10-30): exhausted
tree=36f9a57f022d88e331ca834976f4e1deaf8a57a4

Trigger: `scripts/s9-census-due.sh` → DUE (base ed2227a64321, reason=plan-relevant-change: `2738001df` touched
internal/optimizer/planner.go, internal/parser and internal/catalog for ALTER PUBLICATION, M0122-0014). This is an
unchanged census under S9(4a): only this file is committed, and the captures were deleted from `tmp/`.

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-H | 22 / 22 | all 0 |
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |

`scripts/s9-census-due.sh --compare tmp/s9w18-census` (exit 0):

- tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt
- tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt
- tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt

Engine binary (AGENT.md G3) and capture repo head, per corpus:

- tpch: tmp/goopg-acceptance-bin sha256 ab9cdf9e1399470c21ed9c259b35c8c4d4cf41232a1d5040c9968038487c2b87 (the served
  binary the audit arm verified); repo head e3598f114.
- tpcds-sf025: tmp/goopg-jointree-bin sha256 ab9cdf9e1399470c21ed9c259b35c8c4d4cf41232a1d5040c9968038487c2b87;
  engine-id f08833b6183c…; repo head e3598f114.
- tpcds-sf1: the same binary and engine-id; repo head e3598f114.
