# M0146-0014 — census at 56dea44e6 (2026-10-31): exhausted

Trigger: `scripts/s9-census-due.sh` returned DUE (base 9cff2a642837, reason=plan-relevant-change). `56dea44e6`
changed internal/storage/bufpool.go and prefetch.go — the truncate/eviction flush-race fix
(AI-20261031-002109-005); bufpool.go sits in the trigger set (all-visible / block counts). No `tree=` line:
the capture's binaries were built while the working tree carried uncommitted in-progress
ALTER SUBSCRIPTION edits under internal/parser and internal/catalog (S9(4a): write `tree=` only when
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` holds). This is an unchanged census under
S9(4a), so only this file is committed; the captures were deleted from `tmp/`.

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-H | 22 / 22 | all 0 |
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |

`scripts/s9-census-due.sh --compare tmp/s9w21-census` (exit 0):

- tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt
- tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt
- tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt

Engine binary (AGENT.md G3) and capture repo head, per corpus:

- tpch: tmp/goopg-acceptance-bin sha256 58f637c921a315845e237082841471df059bb0fd9b006a00410340ec1def0423 (the
  audit arm's lane binary, built from HEAD's tree at capture start); repo head 56dea44e6.
- tpcds-sf025 and tpcds-sf1: tmp/goopg-jointree-bin sha256
  e76ee673458c1ae390383b9bfeb959c9ba089ecf82251170b82c225709fd43e6; repo head 56dea44e6.

Both binaries differ from the routing-20261030f capture's 1631de084d8b930a12c246b60b1cb7d499d5d1e40c2ea5cf194ac3fa2cc512ad
— a new binary was measured (G3), and the plan text is byte-identical to the base on all three corpora.

Nothing filed: every CATEGORIES-EXCL-MATCH category is 0 on every corpus, so no record is actionable.
The id(gate) list is unchanged from the routing-20261030f note (which names the 20261030a note).
