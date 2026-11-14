# M0146-0014 — census at 0d3dc635d (2026-11-15): exhausted tree=a0271004611080a6f3c9eef79322bae9bcabafbb

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115c, reason=plan-relevant-change)
— code commit `0d3dc635d` (M0148-0001c3a9 slice 4: child index paths, mixed-width Append fix).
The trigger files are `internal/optimizer/path.go` and
`internal/optimizer/partitionwisejoin.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `1ead3cd80`.

Capture: `BASELINE_REV=30ef7ad0e CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dh tmp/census-20261115d`
(BASELINE_REV = the head consumed by routing-20261115c; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dh-candidate`
sha256 `21f78fe62ba4f554d8c40f71744bcaed8d9ee8bd557fcb2af3bb695f336ad1f2`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115d/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115c's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
