# M0146-0014 — census at f9b00dc8d (2026-11-16): exhausted tree=4a39b94ba0f74dcd769d74d7c4f6fae8faf494d3

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116z, reason=plan-relevant-change)
— code commit `f9b00dc8d` (M0149-0001g slice 25: measured relpages/reltuples in bootstrapped pg_class rows).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `f9b00dc8d`.

Capture: `BASELINE_REV=5e96bbf98 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ey tmp/census-20261116za`
(BASELINE_REV = the head consumed by routing-20261116z; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ey-candidate`
sha256 `19eb4ee3a30deba0bdfcc37cd0ee75278a04f4424656004397325e88b42b361b`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116za/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116z's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
