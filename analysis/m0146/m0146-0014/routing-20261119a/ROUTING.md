# M0146-0014 — census at 385b88476 (2026-11-19): exhausted tree=836abf29ac05d4f200ea2464436e96bffbd1eba4

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118x, reason=plan-relevant-change)
— code commit `385b88476` (M0149-0001h1a51: timestamptz arithmetic in the session zone).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `385b88476`.

Capture: `BASELINE_REV=e7899edfd CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ia tmp/census-20261119a`
(BASELINE_REV = the head consumed by routing-20261118x; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ia-candidate`
sha256 `9241b34f10b5f1b36afa8b71c350e9a4b7bc8cb4557ef3a8513e413059050232`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119a/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118x's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
