# M0146-0014 — census at 7d764e07f (2026-11-14): exhausted tree=84a8dcc5876755cdf3636c72868c61ea00e98e6f

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114g, reason=plan-relevant-change)
— code commit `7d764e07f` (M0148-0001c3a4 item (j): PG 18 skip scan with a range on a later index column).
The trigger files include `internal/optimizer/pathindexrestrict.go`, `pathindexonly.go`, `pathbitmap.go` and
`internal/executor/operators_index.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `f7e010c4b`.

Capture: `BASELINE_REV=3ab65d690 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9cm tmp/census-20261114h`
(BASELINE_REV = the head consumed by routing-20261114g; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9cm-candidate`
sha256 `260919fd91b4b0316eaea251d490a354e93cd2d584377b00378763e4ee65c563`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114h/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114g's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
