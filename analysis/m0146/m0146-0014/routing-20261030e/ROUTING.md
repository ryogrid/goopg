# M0146-0014 — census at cfc5b5497 (2026-10-30): exhausted
tree=10df544e684404d1a4f9f3179125720cffd0587c

Trigger: `scripts/s9-census-due.sh` returned DUE (base 36f9a57f022d, reason=plan-relevant-change). `32bbaaf04` touched
grammar/, internal/parser, internal/optimizer/planner.go and internal/catalog for the publication DDL forms
(M0122-0014). This is an unchanged census under S9(4a), so only this file is committed; the captures were deleted
from `tmp/`.

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-H | 22 / 22 | all 0 |
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |

`scripts/s9-census-due.sh --compare tmp/s9w19-census` (exit 0):

- tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt
- tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt
- tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt

Engine binary (AGENT.md G3) and capture repo head, per corpus:

- tpch: tmp/goopg-acceptance-bin sha256 1a97e557e76b1e751ab247f17f5f5245dd49bfd5a9768c4162ec008672f1373a (the served
  binary the audit arm verified); repo head cfc5b5497.
- tpcds-sf025 and tpcds-sf1: tmp/goopg-jointree-bin sha256
  1a97e557e76b1e751ab247f17f5f5245dd49bfd5a9768c4162ec008672f1373a; engine-id 90c6d7a91a1e…; repo head cfc5b5497.
