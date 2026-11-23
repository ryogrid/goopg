# M0146-0014 — census at c9e7861d5 (2026-11-19): exhausted tree=3b5638b7f8aec88394da17d87b28e79e584f9107

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123y, reason=plan-relevant-change)
— code commit `c9e7861d5` (M0149-0012: standby snapshots + 2PC replay).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `c9e7861d5`.

Capture: `BASELINE_REV=b6bfa4a48 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iz tmp/census-20261123z`
(BASELINE_REV = the head consumed by routing-20261123y; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iz-candidate`
sha256 `19140a2254817b4b8a0a490f4c7e64107fee0cdca423bbea481516271a4faade`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123z/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123y's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
