# M0146-0014 — census at 2bf9fe449 (2026-11-15): exhausted tree=7f8e6532d75024256dda4bb7d5f52f95160091ac

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115o, reason=plan-relevant-change)
— code commit `2bf9fe449` (M0149-0001a1 S2: pg_database shows postgres as oid 5) and the owner
commit `08d588030` (standby promote signal file name).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `2bf9fe449`.

Capture: `BASELINE_REV=9c5ea7b23 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dt tmp/census-20261115p`
(BASELINE_REV = the head consumed by routing-20261115o; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dt-candidate`
sha256 `98800e624ce61db6b30a2bb2b19a9d43e8e908e6a6925e338e5924a85e411e44`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115p/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115o's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
