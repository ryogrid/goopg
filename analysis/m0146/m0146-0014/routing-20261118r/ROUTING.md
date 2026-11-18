# M0146-0014 — census at 6526601b6 (2026-11-18): exhausted tree=8452a177fc853b672816d4eb176dabf18c5af41d

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118q, reason=plan-relevant-change)
— code commit `6526601b6` (M0149-0001h1a39: domain typmods on columns and casts; ALTER COLUMN TYPE validation).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `6526601b6`.

Capture: `BASELINE_REV=3c844e578 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hr tmp/census-20261118r`
(BASELINE_REV = the head consumed by routing-20261118q; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hr-candidate`
sha256 `9e81a2c1fe2fcda0f92e03222ebc3f238e209c4c9256f162ec00a532b7fef3fe`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118r/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118q's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
