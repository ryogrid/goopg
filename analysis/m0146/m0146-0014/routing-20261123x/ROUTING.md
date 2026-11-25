# M0146-0014 — census at 4ef5fe018 (2026-11-19): exhausted tree=53330f06e2f5f8f7cd293ec9d730bb26ba48da59

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123w, reason=plan-relevant-change)
— code commit `4ef5fe018` (M0149-0010b: FPI zero-extend, pg_attribute decode, FSM checksums).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `4ef5fe018`.

Capture: `BASELINE_REV=16fd35c92 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ix tmp/census-20261123x`
(BASELINE_REV = the head consumed by routing-20261123w; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ix-candidate`
sha256 `93fe3015e96574f79947cb02807b52e367baa29dfeaf867312034bf9183a18a0`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123x/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123w's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
