# M0146-0014 — census at b19957da3 (2026-11-14): exhausted tree=25a4f7d8ff122b7c7550290b8cc7a974afa75182

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114v, reason=plan-relevant-change)
— code commit `b19957da3` (M0148-0001c3a12 slice 1: row-comparison index quals on Index Scan / Index Only Scan).
The trigger files include `internal/optimizer/pathindexrestrict.go`, `internal/optimizer/selectivity.go` and
`internal/executor/index_trailing_saop.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `704f8f798`.

Capture: `BASELINE_REV=a7fd3bccf CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9db tmp/census-20261114w`
(BASELINE_REV = the head consumed by routing-20261114v; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9db-candidate`
sha256 `e7d54a58c07b47f02b2b4cbffe3f9def75d76f8204e6d5daac40dd1f7a3fc12a`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114w/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114v's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
