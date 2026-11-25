# M0146-0014 — census at bfba991eb (2026-11-17): exhausted tree=d51b61aca90c261105e8cfefae94ff78d550c5b4

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117x, reason=plan-relevant-change)
— code commit `bfba991eb` (M0149-0001h1a15: pg_type typmodin / typmodout / typanalyze from a PG 18.3 initdb, user array typanalyze / typsubscript).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `bfba991eb`.

Capture: `BASELINE_REV=baca29aa6 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gy tmp/census-20261117y`
(BASELINE_REV = the head consumed by routing-20261117x; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gy-candidate`
sha256 `47a0c0ea3a8de056b234baf906826c241108f2bf52246af6658e4ba3a61cb4fb`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117y/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117x's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
