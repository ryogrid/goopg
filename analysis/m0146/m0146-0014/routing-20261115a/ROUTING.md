# M0146-0014 — census at 7b02fea41 (2026-11-15): exhausted tree=5db079c24ff506665a17be6f11b835576f4bedf7

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114y, reason=plan-relevant-change)
— code commit `7b02fea41` (M0148-0001c3a9 slice 1: partition members in PartitionDesc order).
The trigger files are `internal/optimizer/planner.go` and
`internal/optimizer/partitiondesc.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `6083698be`.

Capture: `BASELINE_REV=073b218c6 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9de tmp/census-20261115a`
(BASELINE_REV = the head consumed by routing-20261114y; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9de-candidate`
sha256 `9a176ddd3241042dbb17a43a29ec422e74279cac57b32174376b1226bee88694`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115a/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114y's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
