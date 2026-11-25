# M0146-0014 — census at d52570530 (2026-11-14): exhausted tree=fc3781e5ef99feac87d14d96447d31a56f15e0aa

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114l, reason=plan-relevant-change)
— code commit `d52570530` (M0148-0001c3a5 item (a) slice 2: PG index column names via FigureIndexColname, expression ORDER BY keys in the partition-ordered Append).
The trigger files include `internal/optimizer/orderedpartitionappend.go`, `internal/parser/figurecolname.go` (new) and
`internal/executor/operators_ddl.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `39bb0f4e8`.

Capture: `BASELINE_REV=e4787ad9e CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9cr tmp/census-20261114m`
(BASELINE_REV = the head consumed by routing-20261114l; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9cr-candidate`
sha256 `b93886d0eb6801f3a6d670e642fdde1c8b5032c0da0ba37fc820be90683b8d7e`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114m/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114l's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
