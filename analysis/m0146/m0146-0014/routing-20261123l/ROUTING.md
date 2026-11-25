# M0146-0014 — census at 823739b59 (2026-11-19): exhausted tree=7a00277f2f1f166d9ca6167948d4907ccde9df4f

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123k, reason=plan-relevant-change)
— code commit `823739b59` (M0149-0002: TRUNCATE/VACUUM truncate WAL, freeze plans).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `823739b59`.

Capture: `BASELINE_REV=3e940f0c7 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9il tmp/census-20261123l`
(BASELINE_REV = the head consumed by routing-20261123k; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9il-candidate`
sha256 `2b3881c96e098e69705ac372ac8bda09f91761026298a99f58a7223f060012fd`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123l/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123k's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
