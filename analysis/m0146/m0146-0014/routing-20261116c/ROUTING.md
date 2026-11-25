# M0146-0014 — census at 54c05f6a7 (2026-11-16): exhausted tree=7cd3e46dd88fa8413dd19de8fc712297656b28dd

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116b, reason=plan-relevant-change)
— code commit `54c05f6a7` (M0149-0001g slice 1: PG-shaped catalog descriptors, pg_attribute_relid_attnam_index,
local relhasindex).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `54c05f6a7`.

Capture: `BASELINE_REV=e11971ba1 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ea tmp/census-20261116c`
(BASELINE_REV = the head consumed by routing-20261116b; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ea-candidate`
sha256 `91c70121d8d3f8df9723d360841d107f9ebe9ba047c59ebdff8e4e52e91bbc68`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116c/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116b's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
