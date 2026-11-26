# M0146-0014 — census at 2753ae9cc (2026-11-19): exhausted tree=2a0da6ec7f2915c9cc8642ac5347d33d0580a7c6

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261127c, reason=plan-relevant-change)
— code commit `2753ae9cc` (M0149-0057a/b/e/f apply-as-owner checks + partition routing + owner REVOKE).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `2753ae9cc`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9id tmp/census-20261127d`
(BASELINE_REV = the head consumed by routing-20261127c; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9id-candidate`
sha256 `ae3c9896ffd48bbe0aa912156c07e635d13e8b270e1d189a1a86d53be1fb2c9a`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261127d/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261127c's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
