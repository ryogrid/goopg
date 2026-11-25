# M0146-0014 — census at d0a79274a (2026-11-19): exhausted tree=f867fb88da71956fbbcba00efe7b7a17238843f2

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124a, reason=plan-relevant-change)
— code commit `d0a79274a` (M0149-0014: sync rep slots + cluster_name).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `d0a79274a`.

Capture: `BASELINE_REV=0b1c0660c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ib tmp/census-20261124b`
(BASELINE_REV = the head consumed by routing-20261124a; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ib-candidate`
sha256 `af11db5f71e112e31295bcfaf7471cc1414f593a1f77243894873a51772bbe6e`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124b/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124a's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
