# M0146-0014 — census at c578939b9 (2026-11-19): exhausted tree=e7146a635a153c3b9a7368e0dbecc36e7e0f7997

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119c, reason=plan-relevant-change)
— code commit `c578939b9` (M0149-0001h1a63: untyped literals opposite timestamps).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `9effb193a`.

Capture: `BASELINE_REV=2e73c299c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9id tmp/census-20261119d`
(BASELINE_REV = the head consumed by routing-20261119c; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9id-candidate`
sha256 `6ed79b1ea283af421dfb31047f3bbf7a0acc67e9b8535b714c4f3254004b66ab`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119d/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119c's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
