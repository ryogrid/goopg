# M0146-0014 — census at 6462c0800 (2026-11-18): exhausted tree=56350fdeafbca6d48843a708b91b01846633d907

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118k, reason=plan-relevant-change)
— code commits `38b9618ad` (M0149-0001h1a31: partition functions walk OIDs; per-object regclassout), `5cecc2def` (pubsubcluster harness) and `6462c0800` (M0149-0001h1a32: pg_*_is_visible and reg* input follow search_path).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `6462c0800`.

Capture: `BASELINE_REV=617f1f80d CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hl tmp/census-20261118l`
(BASELINE_REV = the head consumed by routing-20261118k; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hl-candidate`
sha256 `7aef61f0aed85011247d05001855ce2833fb1a1302788a05b1dbc6be76dc607c`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118l/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118k's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
