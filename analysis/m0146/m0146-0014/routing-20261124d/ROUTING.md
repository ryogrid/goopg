# M0146-0014 — census at 02922370f (2026-11-19): exhausted tree=22724db40d545cfcd41b6c4e28a158f72c8e4ec2

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124c, reason=plan-relevant-change)
— code commit `02922370f` (M0149-0015a1: 2PC WAL records).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `02922370f`.

Capture: `BASELINE_REV=9deacbc00 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9id tmp/census-20261124d`
(BASELINE_REV = the head consumed by routing-20261124c; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9id-candidate`
sha256 `4c8399efd67a815df4df8ba4ad4543316b53e23e4d1673e1399750bfcaf3837f`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124d/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124c's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
