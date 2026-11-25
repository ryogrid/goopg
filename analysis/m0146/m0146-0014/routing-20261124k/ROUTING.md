# M0146-0014 — census at c961a49b0 (2026-11-19): exhausted tree=08e1a8102bf86904f2fdd7936d5bfc307e2864ea

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124j, reason=plan-relevant-change)
— code commit `c961a49b0` (M0149-0017a: end of WAL at a missing segment, OVERWRITE_CONTRECORD).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `c961a49b0`.

Capture: `BASELINE_REV=7139c4ab9 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ik tmp/census-20261124k`
(BASELINE_REV = the head consumed by routing-20261124j; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ik-candidate`
sha256 `625a0164fb56b2e024929ea458a5bb0e092140d68e7b71db8499eea83a554554`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124k/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124j's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
