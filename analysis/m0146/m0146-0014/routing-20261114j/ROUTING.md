# M0146-0014 — census at 691fa2a27 (2026-11-14): exhausted tree=d3cab9b7c9f3db89f721d4407beb62bd2ac2c9d3

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114i, reason=plan-relevant-change)
— code commit `691fa2a27` (M0148-0001c3a5 item (d): boolean index quals and constant-column index pathkeys).
The trigger files include `internal/optimizer/pathkeysindex.go`, `pathindexrestrict.go`, `pathindexordered.go` and
`internal/optimizer/pathindexonly.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `13ed065fb`.

Capture: `BASELINE_REV=73eb342b2 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9co tmp/census-20261114j`
(BASELINE_REV = the head consumed by routing-20261114i; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9co-candidate`
sha256 `714245881868806fbd57b7ae17c6a52f41968d436dcf2eced47099bc99828bb8`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114j/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114i's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
