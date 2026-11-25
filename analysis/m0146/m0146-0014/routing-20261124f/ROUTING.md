# M0146-0014 — census at 13a991517 (2026-11-19): exhausted tree=c31683deb2ac723a7389c6b5140d73d0eacdad45

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124e, reason=plan-relevant-change)
— code commit `13a991517` (M0149-0015b/e + 0016 slice 1).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `13a991517`.

Capture: `BASELINE_REV=7d5e3f751 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9if tmp/census-20261124f`
(BASELINE_REV = the head consumed by routing-20261124e; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9if-candidate`
sha256 `27a8fe65e8c00798ac163420447242ac7aa440d5253c4c11883bd263422b7aed`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124f/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124e's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
