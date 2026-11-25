# M0146-0014 — census at fc9554b07 (2026-11-18): exhausted tree=6ce7ff7a6a0b46081c3ea1733036521a3dfa7912

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118g, reason=plan-relevant-change)
— code commit `fc9554b07` (M0149-0001h1a26: pg_class.relowner from the creating role; partitioned index relkind I).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `fc9554b07`.

Capture: `BASELINE_REV=476dc5f1f CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hh tmp/census-20261118h`
(BASELINE_REV = the head consumed by routing-20261118g; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hh-candidate`
sha256 `8919a5fac5667e810598aaf47bf603d3e3870a372696776b8fa57ccf49e887d2`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118h/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118g's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
