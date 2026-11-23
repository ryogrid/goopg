# M0146-0014 — census at 3df0353fb (2026-11-19): exhausted tree=3752b5113f5c4abec80c62f101bafdb96bd363f9

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123p, reason=plan-relevant-change)
— code commit `3df0353fb` (M0149-0006a: temp relation naming + startup sweep).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `3df0353fb`.

Capture: `BASELINE_REV=7c6c60604 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iq tmp/census-20261123q`
(BASELINE_REV = the head consumed by routing-20261123p; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iq-candidate`
sha256 `49e172506a438c6e72f242ed78bc88a498287419bec3121f0e1e3b95e37f9f37`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123q/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123p's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
