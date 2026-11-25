# M0146-0014 — census at 9c5ea7b23 (2026-11-15): exhausted tree=b56c68a7b9e97741cca79aabc4175ff42e22f779

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115n, reason=plan-relevant-change)
— code commit `9c5ea7b23` (M0149-0001a1 S1: postgres catalogs stored and WAL-logged at DBOid 5).
The trigger files include
`internal/initdb/open.go`, `internal/initdb/layout_postgres_base5.go`, `internal/storage/bufpool.go`, `internal/storage/vm.go`. The census plan A/B measured
`fires=none` on all three corpora.
Capture repo head `9c5ea7b23` (HEAD tree above). The worktree also carried a concurrent
session's unstaged, plan-irrelevant WIP (`cmd/goopg/standby.go`, `cmd/goopg/standby_test.go`,
`internal/initdb/standby.go`: the promote signal file name), so the candidate engine was built from
HEAD plus that diff; the gate stamped FAIL for that reason only (verdict PASS).

Capture: `BASELINE_REV=15801d82e CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ds tmp/census-20261115o`
(BASELINE_REV = the head consumed by routing-20261115n; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ds-candidate`
sha256 `00dd63145cb8db9ef1aede2bf30a924c8b89f50e725119b01e865887f549e7a2`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115o/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115n's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
