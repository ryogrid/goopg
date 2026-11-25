# M0146-0014 — census at 3d78ef485 (2026-11-19): exhausted tree=ab0e5d3b1f88d6c9a60f568e79e898a8a19a4669

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123g, reason=plan-relevant-change)
— code commit `3d78ef485` (M0149-0001h1a85: typed literals of user types).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `3d78ef485`.

Capture: `BASELINE_REV=8108a9209 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ih tmp/census-20261123h`
(BASELINE_REV = the head consumed by routing-20261123g; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ih-candidate`
sha256 `589cf8423c51782b751ca045578b82d0ba5a6949e869ea20cd0af652c5249076`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123h/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123g's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
