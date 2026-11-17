# M0146-0014 — census at a58f714ee (2026-11-17): exhausted tree=b37ccf237b12773e7895edac1db78fb4180c132a

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117r, reason=plan-relevant-change)
— code commit `a58f714ee` (M0149-0001h1a6: large-object default privileges, transactional pg_default_acl).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `a58f714ee`.

Capture: `BASELINE_REV=100a4033c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gs tmp/census-20261117s`
(BASELINE_REV = the head consumed by routing-20261117r; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gs-candidate`
sha256 `557fceb1222e007536ca839a01ea062f35e463d5617db5239a1c9046920f5d86`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117s/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117r's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
