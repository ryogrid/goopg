# M0146-0014 — census at 565905450 (2026-11-17): exhausted tree=20110850b1861b9e517037bfe6f4d70e8c193a69

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117i, reason=plan-relevant-change)
— code commit `565905450` (M0149-0001h1 slice 2: has_column / any_column / sequence / function / type_privilege).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `565905450`.

Capture: `BASELINE_REV=89012e2ea CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gj tmp/census-20261117j`
(BASELINE_REV = the head consumed by routing-20261117i; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gj-candidate`
sha256 `8751f3c8a6f2b70e1e4bdac67072902a21513c28e6fc8e89238b31d5a1e7828e`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117j/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117i's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
