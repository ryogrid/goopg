# M0146-0014 — census at 8329da6c9 (2026-11-15): exhausted tree=bb89066427648c0ea40ee170f7c37bd3164834ab

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115h, reason=plan-relevant-change)
— code commit `8329da6c9` (M0148-0001c3a10 slice 5: Merge Append over the partitions' ordered child paths).
The trigger files are
`internal/optimizer/createplanpartitionwise.go`, `internal/optimizer/partitionwisejoin.go`, `internal/optimizer/path.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `8329da6c9`.

Capture: `BASELINE_REV=47d736afb CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dm tmp/census-20261115i`
(BASELINE_REV = the head consumed by routing-20261115h; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dm-candidate`
sha256 `6c00187b8edc5440a6ef637f9719608ea2c6c8779c641e19998b84d68feab615`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115i/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115h's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
