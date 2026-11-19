# M0146-0014 — census at a4d4fc494 (2026-11-19): exhausted tree=e1f6e31674ad7bca9f94e92ed628eb5885433069

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119t, reason=plan-relevant-change)
— code commit `a4d4fc494` (M0149-0001h1a74: time values print as time_out).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `a4d4fc494`.

Capture: `BASELINE_REV=cc0c0c190 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iu tmp/census-20261119u`
(BASELINE_REV = the head consumed by routing-20261119t; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iu-candidate`
sha256 `69d6fa9e7bb1ec03b9fc437944f54be32476965bf1ab261e513f69932a00baaf`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119u/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119t's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
