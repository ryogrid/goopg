# M0146-0014 — census at 010478219 (2026-11-19): exhausted tree=818403d01b5efab3688d8b3f5e2f2e2b1c9368c7

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123t, reason=plan-relevant-change)
— code commit `010478219` (M0149-0076: pg_control_checkpoint, pg_settings presets).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `010478219`.

Capture: `BASELINE_REV=f9dc66d14 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iu tmp/census-20261123u`
(BASELINE_REV = the head consumed by routing-20261123t; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iu-candidate`
sha256 `c48649bf1b12782a2c77c42f786b779dc0aaa604ad4350104a95691fc9c880de`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123u/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123t's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
