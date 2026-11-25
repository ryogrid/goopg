# M0146-0014 — census at 668e5bf0b (2026-11-19): exhausted tree=9ee599f32a5ab2c3e884520a5e56b14b42a1b080

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124r, reason=plan-relevant-change)
— code commit `668e5bf0b` (hot_standby_feedback + walreceiver reload restart (M0149-0077i)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `668e5bf0b`.

Capture: `BASELINE_REV=01e00f662 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9is tmp/census-20261124s`
(BASELINE_REV = the head consumed by routing-20261124r; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9is-candidate`
sha256 `c75d9a99fe0a48c345613af54793bac95a45839492af7dd0ccfb6c21c1639c8d`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124s/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124r's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
