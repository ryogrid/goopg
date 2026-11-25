# M0146-0014 — census at da6e70237 (2026-11-15): exhausted tree=1d92510fcde11f45fe0bd239ca366a9b32d23495

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115d, reason=plan-relevant-change)
— code commit `da6e70237` (M0148-0001c3a10 slice 1: partition child paths for a restricted single-relation partitioned table).
The trigger files include `internal/optimizer/relfromjoinlist.go` and
`internal/optimizer/partitionwisejoin.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `eefb34976`.

Capture: `BASELINE_REV=1ead3cd80 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9di tmp/census-20261115e`
(BASELINE_REV = the head consumed by routing-20261115d; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9di-candidate`
sha256 `21c5c9890e2136512a351443300916d4c243b541b4759761205f7a4644a0d6ae`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115e/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115d's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
