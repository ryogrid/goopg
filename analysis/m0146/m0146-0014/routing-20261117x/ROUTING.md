# M0146-0014 — census at baca29aa6 (2026-11-17): exhausted tree=2918706ee33c51ba0242022fa816845df8c71cb4

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117w, reason=plan-relevant-change)
— code commit `baca29aa6` (M0149-0001h1a14: regoperin / regoperatorin / regoperout over the full pg_operator seed, to_regoper / to_regoperator).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `baca29aa6`.

Capture: `BASELINE_REV=678dfeb64 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gx tmp/census-20261117x`
(BASELINE_REV = the head consumed by routing-20261117w; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gx-candidate`
sha256 `af14862c918e0cdac5a0b3b1f8bb381371050325753913cacaaa84818cc11932`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117x/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117w's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
