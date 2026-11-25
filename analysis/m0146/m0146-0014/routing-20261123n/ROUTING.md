# M0146-0014 — census at 79f9998bb (2026-11-19): exhausted tree=6e86e88805a2dd37df48c81c3c62e8d07e8fb4e2

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123m, reason=plan-relevant-change)
— code commit `79f9998bb` (M0149-0005: invalid database, 2PC lock hand-off).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `79f9998bb`.

Capture: `BASELINE_REV=fa3584a44 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9in tmp/census-20261123n`
(BASELINE_REV = the head consumed by routing-20261123m; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9in-candidate`
sha256 `8ed5a27ca67119626bae666b187068d6321c9fbc163d1675de2216ed8a7d2f55`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123n/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123m's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
