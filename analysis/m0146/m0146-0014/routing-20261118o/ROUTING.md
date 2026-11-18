# M0146-0014 — census at 7a6a0a482 (2026-11-18): exhausted tree=b68b261cdb2d40c34c5c88f192f36700a827e756

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118n, reason=plan-relevant-change)
— code commit `7a6a0a482` (M0149-0001h1a35: pg_class lists the 415 system relations; column label of casts to "char").
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `7a6a0a482`.

Capture: `BASELINE_REV=b55715aa3 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ho tmp/census-20261118o`
(BASELINE_REV = the head consumed by routing-20261118n; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ho-candidate`
sha256 `16b2d6d87600ada496aed1cdb29f882107c07b97839e5799130b51deda6ea1fb`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118o/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118n's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
