# M0146-0014 — census at 2e9e4f5d6 (2026-11-14): exhausted tree=cc86dbdf339d1a5bc415b3ea9c50c28da1a510e9

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114w, reason=plan-relevant-change)
— code commit `2e9e4f5d6` (M0148-0001c3a12 slice 2: bitmap row-comparison index quals; DML qual reconstruction).
The trigger files include `internal/optimizer/pathbitmap.go`, `internal/optimizer/createplanbitmap.go`,
`internal/optimizer/plan.go` and `internal/executor/operators_explain.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `38b072965`.

Capture: `BASELINE_REV=704f8f798 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dc tmp/census-20261114x`
(BASELINE_REV = the head consumed by routing-20261114w; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dc-candidate`
sha256 `43a1c5df2a6c861c0145a2c5da7cae9a4289bde61b7b8e2dfbc190d87f616e9b`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114x/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114w's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
