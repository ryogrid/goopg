# M0146-0014 — census at 01e00f662 (2026-11-19): exhausted tree=6f41b26eaa7f8da799415afd9136f418704413f8

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124q, reason=plan-relevant-change)
— code commit `01e00f662` (standby event trigger refresh (M0149-0077h)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `01e00f662`.

Capture: `BASELINE_REV=746e797da CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ir tmp/census-20261124r`
(BASELINE_REV = the head consumed by routing-20261124q; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ir-candidate`
sha256 `19febf2778ac76596a1f7e33971565d980d63c5063682ebf148b12e17dd0f13c`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124r/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124q's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
