# M0146-0014 — census at 1cf76a7d0 (2026-11-18): exhausted tree=50ad8637afed0529283ccc787dcf09710a9b80f0

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118d, reason=plan-relevant-change)
— code commit `1cf76a7d0` (M0149-0001h1a22: pg_conversion / pg_range built-in rows, oidvector 0-based subscripts, regtypein over every built-in name).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `1cf76a7d0`.

Capture: `BASELINE_REV=468867019 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9he tmp/census-20261118e`
(BASELINE_REV = the head consumed by routing-20261118d; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9he-candidate`
sha256 `3d03c1fbe6634c4c4bba819803858164b59c98b73cb99f97f09fe9fc168fadce`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118e/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118d's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
