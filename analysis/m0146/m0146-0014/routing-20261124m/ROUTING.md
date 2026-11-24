# M0146-0014 — census at 28ec638ad (2026-11-19): exhausted tree=f6141919187381249416f4d77d0e86f96470c2fe

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124l, reason=plan-relevant-change)
— code commit `28ec638ad` (M0149-0077 slice 1: pg_sequence_last_value).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `28ec638ad`.

Capture: `BASELINE_REV=d8fd3d5a5 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9im tmp/census-20261124m`
(BASELINE_REV = the head consumed by routing-20261124l; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9im-candidate`
sha256 `fb89ac1cacec50d468e81209395c892eeaac1d58b83a91b8ca882f843ce96480`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124m/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124l's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
