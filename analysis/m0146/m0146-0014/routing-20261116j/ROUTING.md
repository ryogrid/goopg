# M0146-0014 — census at 85019101c (2026-11-16): exhausted tree=627651c3d3ee94740bd2611a8650c53d112c703a

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116i, reason=plan-relevant-change)
— code commit `85019101c` (M0149-0001g slice 8: pg_am/pg_amop/pg_amproc pinned to postgres.bki).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `85019101c`.

Capture: `BASELINE_REV=6ffd8f664 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9eh tmp/census-20261116j`
(BASELINE_REV = the head consumed by routing-20261116i; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9eh-candidate`
sha256 `16cff5947d3980cb28b68b390cf2622a7677050deba30437983663323f500163`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116j/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116i's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
