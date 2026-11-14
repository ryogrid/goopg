# M0146-0014 — census at 3c0e01d75 (2026-11-14): exhausted tree=f6dbd7ca8f48d770bf5e602167a9fad2f1484eb1

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114o, reason=plan-relevant-change)
— code commit `3c0e01d75` (M0148-0001c3a5 item (a) slice 5: the min/max subquery is planned by the path search).
The trigger files are `internal/optimizer/planner.go` and
`internal/executor/operators_explain.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `2d2aee32a`.

Capture: `BASELINE_REV=e8aa979d6 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9cu tmp/census-20261114p`
(BASELINE_REV = the head consumed by routing-20261114o; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9cu-candidate`
sha256 `3d1bb10e4c9ebde9b258514e0fdae03c1236e213167d8b4685d36a394a7d0b43`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114p/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114o's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
