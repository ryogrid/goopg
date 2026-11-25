# M0146-0014 — census at fcd8aa879 (2026-11-16): exhausted tree=fc811c5e5e8880de81751981d7ab45f2399a9d06

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116ze, reason=plan-relevant-change)
— code commit `fcd8aa879` (M0149-0001g slice 30: pg_largeobject and its index bootstrapped).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `fcd8aa879`.

Capture: `BASELINE_REV=d151c0936 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9fd tmp/census-20261116zf`
(BASELINE_REV = the head consumed by routing-20261116ze; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9fd-candidate`
sha256 `1d2de6a98f493da09601fc836b8d3257c91eebdad0dfcfa027e02e69c5766fdd`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116zf/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116ze's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
