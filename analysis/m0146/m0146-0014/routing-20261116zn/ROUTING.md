# M0146-0014 — census at 06cfa776f (2026-11-16): exhausted tree=ae54960d4e44cb1cfa7e108ae550577fd5400431

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116zm, reason=plan-relevant-change)
— code commit `06cfa776f` (M0149-0001j: name literals read column statistics).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `06cfa776f`.

Capture: `BASELINE_REV=350df0ca8 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9fn tmp/census-20261116zn`
(BASELINE_REV = the head consumed by routing-20261116zm; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9fn-candidate`
sha256 `e28df7262c1c65ae110001c2bb5ff85b9a66a7c546c7769e4db23d31131bdecf`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116zn/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116zm's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
