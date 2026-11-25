# M0146-0014 — census at ed33aa420 (2026-11-18): exhausted tree=3c39138bcb52a53a81a30801d273663f0011a943

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118t, reason=plan-relevant-change)
— code commits `0db407dcf` (M0149-0001h1a48b: microsecond time carrier) and `ed33aa420` (M0149-0001h1a48c: date arithmetic, planner date folding).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `ed33aa420`.

Capture: `BASELINE_REV=952552a54 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hu tmp/census-20261118u`
(BASELINE_REV = the head consumed by routing-20261118t; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hu-candidate`
sha256 `13e6d1f668020b2572b03a5340e9ad4799f493a53b0ff80126432fcb4bce26fe`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118u/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118t's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
