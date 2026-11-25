# M0146-0014 — census at 0420f29b1 (2026-11-19): exhausted tree=6620993a6d48d4e8d0b8d9d773d64a50e578297d

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124v, reason=plan-relevant-change)
— code commit `0420f29b1` (archive feeder flush, pg_wal_lsn_diff, archiver stats reset (M0149-0018b/c, -0019)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `0420f29b1`.

Capture: `BASELINE_REV=c0acd7d43 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iw tmp/census-20261124w`
(BASELINE_REV = the head consumed by routing-20261124v; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iw-candidate`
sha256 `0c8e2c684a41323bced6545bf1f6f18695d84404b0248c133209fd4c43b47ed7`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124w/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124v's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
