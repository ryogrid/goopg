# M0146-0014 — census at b55715aa3 (2026-11-18): exhausted tree=1e6344f174b525bfe7a4f0024ad57c39dce947fb

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118m, reason=plan-relevant-change)
— code commit `b55715aa3` (M0149-0001h1a34 slice 2: user relations get PG's rowtype and array type; composite visibility fix).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `b55715aa3`.

Capture: `BASELINE_REV=d22373e57 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hn tmp/census-20261118n`
(BASELINE_REV = the head consumed by routing-20261118m; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hn-candidate`
sha256 `2e2536b2de41c09d346977b0448d201cc1281874ea8bcb93e4d60501d7c7e0c4`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118n/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118m's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
