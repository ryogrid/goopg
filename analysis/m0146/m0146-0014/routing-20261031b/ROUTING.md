# M0146-0014 — census at c8de1fe4c (2026-10-31): exhausted

Trigger: `scripts/s9-census-due.sh` returned DUE (base 56dea44e6, reason=plan-relevant-change). `c8de1fe4c`
changed `internal/optimizer/planner.go` (the two ALTER SUBSCRIPTION statement types join the DDL planStmt
arm), `internal/parser/ddl.go` + `ast.go`, `internal/catalog/pubsub.go` and `internal/postmaster/dispatch.go`
— the ALTER SUBSCRIPTION ENABLE / DISABLE / SET (slot_name) escape-hatch slice (M0122-0014); planner.go and
pubsub.go sit in the trigger set. tree= below: `git diff --quiet -- internal cmd scripts bench go.mod go.sum`
held while the capture's binaries were built (the code commit was in HEAD, only `.ralph/` records were being
written). This is an unchanged census under S9(4a), so only this file is committed; the captures were deleted
from `tmp/s9-20261031b/`. tree=e3df993166e67fc78c7a8aecdbece6910db2c39a; capture repo head bd20cec5b
(the recording commit on top of c8de1fe4c, `.ralph/` only).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-H | 22 / 22 | all 0 |
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |

`scripts/s9-census-due.sh --compare tmp/s9-20261031b` (exit 0):

- tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt
- tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt
- tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt

Engine binary (AGENT.md G3) and capture repo head, per corpus:

- tpch: tmp/goopg-acceptance-bin sha256 d312cc184b74e27be8198eb2494b6aa9fd35066c7e9cb62006524ff4ef71be6a
  (built from HEAD's tree at capture start; the arm's estimate-audit tmp/estimate-audit sha256
  eb4ba1868564c552612bff269c6fddef3c53efa4d4b489069a57d9e973cd88b2); repo head bd20cec5b.
- tpcds-sf025 and tpcds-sf1: tmp/goopg-jointree-bin sha256
  d312cc184b74e27be8198eb2494b6aa9fd35066c7e9cb62006524ff4ef71be6a; repo head bd20cec5b.

Both binaries differ from the routing-20261031a capture's 58f637c921a315845e237082841471df059bb0fd9b006a00410340ec1def0423 /
e76ee673458c1ae390383b9bfeb959c9ba089ecf82251170b82c225709fd43e6 — a new binary was measured (G3), and the plan
text is byte-identical to the base on all three corpora.

Nothing filed: every CATEGORIES-EXCL-MATCH category is 0 on every corpus, so no record is actionable.
The id(gate) list is unchanged from the routing-20261031a note (which names the 20261030f note).
