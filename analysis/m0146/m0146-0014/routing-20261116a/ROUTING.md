# M0146-0014 — census at 33e23600c (2026-11-16): exhausted tree=ca4174b3ec4139fac1000c3f9762936764d2721f

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115t, reason=plan-relevant-change)
— code commit `33e23600c` (M0149-0001e: pg_attribute system-attribute rows for catalogs and user tables).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `33e23600c`.

Capture: `BASELINE_REV=f93a91a4e CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dy tmp/census-20261116a`
(BASELINE_REV = the head consumed by routing-20261115t; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dy-candidate`
sha256 `1ee5bc3357d268108ae168a7755429519ad2770a9f38ede8a47958e566fdd5da`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116a/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115t's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
