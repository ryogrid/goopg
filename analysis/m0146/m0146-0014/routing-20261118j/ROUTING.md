# M0146-0014 — census at 75a704ff0 (2026-11-18): exhausted tree=239b55bc27db4897539a8f1616029f6be37453ec

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118i, reason=plan-relevant-change)
— code commit `75a704ff0` (M0149-0001h1a29: index partitions linked on PARTITION OF and ATTACH; ATTACHed tables relispartition).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `75a704ff0`.

Capture: `BASELINE_REV=7283038a9 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hj tmp/census-20261118j`
(BASELINE_REV = the head consumed by routing-20261118i; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hj-candidate`
sha256 `16c8b26cfc04e0ca235cd40e7373d1a5fb01aca389e91c66d101efc68973d95c`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118j/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118i's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
