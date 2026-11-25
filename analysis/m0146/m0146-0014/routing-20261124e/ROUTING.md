# M0146-0014 — census at 7d5e3f751 (2026-11-19): exhausted tree=186b4834c103e88ba688a61858d392f6a42a251c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124d, reason=plan-relevant-change)
— code commit `7d5e3f751` (M0149-0015a2: 2PC restart restore).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `7d5e3f751`.

Capture: `BASELINE_REV=02922370f CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ie tmp/census-20261124e`
(BASELINE_REV = the head consumed by routing-20261124d; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ie-candidate`
sha256 `e54dcfe650079813a38f71c819ba3ce2c4b3119ca27b082ea33cbaaea9397eee`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124e/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124d's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
