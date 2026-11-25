# M0146-0014 — census at 350df0ca8 (2026-11-16): exhausted tree=ce1ce031d1680d20f365e8d5f1b703c173c2b0d5

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116zl, reason=plan-relevant-change)
— code commit `350df0ca8` (M0149-0001j: name\[\] element image and typed name stavalues).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `350df0ca8`.

Capture: `BASELINE_REV=b2a85c718 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9fm tmp/census-20261116zm`
(BASELINE_REV = the head consumed by routing-20261116zl; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9fm-candidate`
sha256 `862230bb8337d098b6e1e74870f63e838c7d7a5d14920c7a7afe2d5d7f6fdd8d`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116zm/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116zl's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
