# M0146-0014 — census at d23bc5ee2 (2026-11-15): exhausted tree=6af2b9d1ca489155734feaa24e244f77c64e00fb

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115k, reason=plan-relevant-change)
— code commit `d23bc5ee2` (M0148-0001c3a10 slice 8: LIST partitions_are_ordered).
The trigger file is
`internal/optimizer/partitionwisejoin.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `d23bc5ee2`.

Capture: `BASELINE_REV=7d45399a2 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dp tmp/census-20261115l`
(BASELINE_REV = the head consumed by routing-20261115k; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dp-candidate`
sha256 `bf84c6c2e4f37964a07b00c15d28416ef5b543f2e1567b0b4211f89640e1c53e`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115l/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115k's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
