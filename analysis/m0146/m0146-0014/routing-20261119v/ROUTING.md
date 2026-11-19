# M0146-0014 — census at 92d911ac0 (2026-11-19): exhausted tree=3e81fda3d0a76a8d465312bad02725dec35d7365

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119u, reason=plan-relevant-change)
— code commit `92d911ac0` (M0149-0001h1a76: real zones in the timezone views).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `92d911ac0`.

Capture: `BASELINE_REV=a4d4fc494 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iv tmp/census-20261119v`
(BASELINE_REV = the head consumed by routing-20261119u; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iv-candidate`
sha256 `071b2f03e0a42c3c40a351ffbe1a4deb8e0bee927adb0c485763d3067987608c`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119v/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119u's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
