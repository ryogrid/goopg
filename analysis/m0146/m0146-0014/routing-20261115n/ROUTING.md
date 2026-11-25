# M0146-0014 — census at 15801d82e (2026-11-15): exhausted tree=4ff92239c8e94aaeeacf3ddc668f4618b56a7ab4

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115m, reason=plan-relevant-change)
— code commit `15801d82e` (M0148-0001c3a10 slice 10: constant-pinned partition keys; boolean partition bounds).
The trigger files are
`internal/optimizer/partitionwisejoin.go`, `internal/executor/operators_ddl.go`, `internal/executor/operators_ddl_partition.go`, `internal/executor/operators_storage.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `15801d82e`.

Capture: `BASELINE_REV=99e42ad84 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dr tmp/census-20261115n`
(BASELINE_REV = the head consumed by routing-20261115m; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dr-candidate`
sha256 `97c94faf52199cb49d84e411501a16efaa20cc87a2161980c0ae6b67ee2aa424`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115n/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115m's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
