# M0146-0014 — census at f241646eb (2026-11-19): exhausted tree=3a990355461fce3570e89cf70d5dbde720993d8d

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123c, reason=plan-relevant-change)
— code commit `f241646eb` (M0149-0001h1a79: sum and avg over interval).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `f241646eb`.

Capture: `BASELINE_REV=fe626d939 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9id tmp/census-20261123d`
(BASELINE_REV = the head consumed by routing-20261123c; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9id-candidate`
sha256 `c0b14cae6383620bc727bcc3af64a330f0f7a0c35aa01abc89b1da59925dbf9f`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123d/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123c's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
