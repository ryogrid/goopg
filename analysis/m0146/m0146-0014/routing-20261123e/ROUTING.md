# M0146-0014 — census at 873c60c7c (2026-11-19): exhausted tree=53d8341f80016c680291d2163f0dfaa2c2ea3905

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123d, reason=plan-relevant-change)
— code commit `873c60c7c` (M0149-0001h1a80: function scans follow cost_functionscan).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `873c60c7c`.

Capture: `BASELINE_REV=f241646eb CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ie tmp/census-20261123e`
(BASELINE_REV = the head consumed by routing-20261123d; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ie-candidate`
sha256 `996cc210d205bb620a8a04bdd1a06a0505ca86ef9e2c9cfeeb1d0b06ec19b935`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123e/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123d's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
