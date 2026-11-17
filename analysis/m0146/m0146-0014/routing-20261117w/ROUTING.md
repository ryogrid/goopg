# M0146-0014 — census at 678dfeb64 (2026-11-17): exhausted tree=2399d15ad06f651b307e0ab5f125af7b1d0c8e87

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117v, reason=plan-relevant-change)
— code commit `678dfeb64` (M0149-0001h1a13: regnamespacein, full-seed regprocin / regprocedurein, reg* function forms, to_regtypemod / to_regnamespace).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `678dfeb64`.

Capture: `BASELINE_REV=36d08b8ea CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gw tmp/census-20261117w`
(BASELINE_REV = the head consumed by routing-20261117v; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gw-candidate`
sha256 `e92990832fb197d80c73c03d5cc6d23e2d8eb478dc4e0ffef090d32d33c68750`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117w/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117v's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
