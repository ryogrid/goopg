# M0146-0014 — census at 89012e2ea (2026-11-17): exhausted tree=6a9b376f088b1fc758feb13401c14a5d27b720a0

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117h, reason=plan-relevant-change)
— code commit `89012e2ea` (M0149-0001h1: has_table_privilege / has_schema_privilege).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `89012e2ea`.

Capture: `BASELINE_REV=ea79c6c06 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gi tmp/census-20261117i`
(BASELINE_REV = the head consumed by routing-20261117h; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gi-candidate`
sha256 `ec670d46dfb7f4e92631a9696dda47f17bdbbe23777ae57a875a87aaf41c8a62`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117i/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117h's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
