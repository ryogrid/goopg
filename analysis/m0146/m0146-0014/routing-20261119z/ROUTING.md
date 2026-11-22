# M0146-0014 — census at 3e8a938e3 (2026-11-19): exhausted tree=13b7b4003dbdb00fc856fda2fcfe862e048d6ad0

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119y, reason=plan-relevant-change)
— code commit `3e8a938e3` (M0149-0001h1a78b: ARRAY constructor common type).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `3e8a938e3`.

Capture: `BASELINE_REV=fe6c96be5 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iz tmp/census-20261119z`
(BASELINE_REV = the head consumed by routing-20261119y; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iz-candidate`
sha256 `a22a5caba07b140826ff77834999a214140863835062d0375aa596b0b0253755`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119z/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119y's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
