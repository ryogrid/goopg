# M0146-0014 — census at 9b99f3b68 (2026-11-14): exhausted tree=632c3ad09e165c4e968a988bf94dbb15799a74c9

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114s, reason=plan-relevant-change)
— code commit `9b99f3b68` (M0148-0001c3a6 item (i) slice 3: an OR group of one cast integer type becomes one `= ANY` arm).
The trigger files are `internal/optimizer/pathbitmapor.go` and
`internal/executor/operators_explain.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `f3f880c38`.

Capture: `BASELINE_REV=4c13945ea CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9cy tmp/census-20261114t`
(BASELINE_REV = the head consumed by routing-20261114s; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9cy-candidate`
sha256 `d47e1abe8b53e6a263b4084df28dadbca01c93b9937d16c2709cd3e8f1580f23`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114t/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114s's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
