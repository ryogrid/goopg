# M0146-0014 — census at bc65bd192 (2026-11-16): exhausted tree=a330d202df4d89de0bed4314806f9beb0180f26c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116m, reason=plan-relevant-change)
— code commit `bc65bd192` (M0149-0001g slice 12: catalog relacl, empty-ACL shape, null-aware pg_class writer).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `bc65bd192`.

Capture: `BASELINE_REV=89fc89594 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9el tmp/census-20261116n`
(BASELINE_REV = the head consumed by routing-20261116m; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9el-candidate`
sha256 `00356069123b3c620f4dc4ee7c212921fe3519d7a57c2c893d9f42d8e40dc5f1`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116n/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116m's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
