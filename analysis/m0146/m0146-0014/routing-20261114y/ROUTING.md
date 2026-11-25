# M0146-0014 — census at 16fdb9bcf (2026-11-14): exhausted tree=b14e5fde2690f3d4c37c68ac9a349078aa17e7ab

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114x, reason=plan-relevant-change)
— code commit `16fdb9bcf` (M0148-0001c3a13: row IN lists as per-column comparisons).
The trigger files are `internal/optimizer/planner.go` and
`internal/executor/operators_explain.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `073b218c6`.

Capture: `BASELINE_REV=38b072965 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dd tmp/census-20261114y`
(BASELINE_REV = the head consumed by routing-20261114x; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dd-candidate`
sha256 `c74da61edba130618f4f2155327aff4e000dd2c41f3d727883eba3c051e80d3f`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114y/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114x's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
