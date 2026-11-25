# M0146-0014 — census at 8fe3291d0 (2026-11-16): exhausted tree=bbf3f0eaf57284ba42c26e25993c66de7716202e

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116za, reason=plan-relevant-change)
— code commit `8fe3291d0` (M0149-0001g slice 26: all 39 catalog TOAST relations).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `8fe3291d0`.

Capture: `BASELINE_REV=f9b00dc8d CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ez tmp/census-20261116zb`
(BASELINE_REV = the head consumed by routing-20261116za; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ez-candidate`
sha256 `8483e2ad87224a4e3bf3a1b0a0e2180cb70b9ec8427b148ab1428371124f304a`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116zb/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116za's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
