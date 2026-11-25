# M0146-0014 — census at 027634bcd (2026-11-19): exhausted tree=f1bdb4b2d8727caeb086eb5788068bfc47f146cc

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261125a, reason=plan-relevant-change)
— code commit `027634bcd` (BASE_BACKUP XLOG_BACKUP_END and faithful pg_control (M0149-0023d)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `027634bcd`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ib tmp/census-20261125b`
(BASELINE_REV = the head consumed by routing-20261125a; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ib-candidate`
sha256 `df76f70f11acbc8f35a06e2b9327f0ab9762f6bead53c15e20d125ced8852127`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261125b/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261125a's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
