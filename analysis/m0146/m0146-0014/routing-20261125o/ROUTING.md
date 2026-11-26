# M0146-0014 — census at eb0ca3f75 (2026-11-19): exhausted tree=d1d711bffaac51218fc9b73b15e9d00353a76f6a

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261125n, reason=plan-relevant-change)
— code commit `eb0ca3f75` (M0149-0046b record_ops keys + IOS composite guard).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `eb0ca3f75`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9io tmp/census-20261125o`
(BASELINE_REV = the head consumed by routing-20261125n; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9io-candidate`
sha256 `07f1e90831fd4ea1c8bf45bfc7d4e5b51fde744dcc2566249db062145fd1dca9`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261125o/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261125n's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
