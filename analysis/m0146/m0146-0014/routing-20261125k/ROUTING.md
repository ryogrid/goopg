# M0146-0014 — census at 53775c316 (2026-11-19): exhausted tree=e5907820d52350fa88ab1d09d27cd78edbb19c0c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261125j, reason=plan-relevant-change)
— code commit `53775c316` (M0149-0032c standby stats drops/restore).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `53775c316`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ik tmp/census-20261125k`
(BASELINE_REV = the head consumed by routing-20261125j; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ik-candidate`
sha256 `6153137d3d6284fda3f059763c5c38c885b464e636db102d51164b4da7dd62e9`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261125k/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261125j's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
