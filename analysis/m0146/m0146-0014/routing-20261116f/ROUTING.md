# M0146-0014 — census at fa6e2bba5 (2026-11-16): exhausted tree=f614e6904547e0d84810e622d841e11936247b42

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116e, reason=plan-relevant-change)
— code commit `fa6e2bba5` (M0149-0001g slice 4: pg_am/pg_amop/pg_amproc/pg_class row-built indexes).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `fa6e2bba5`.

Capture: `BASELINE_REV=0dde92599 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ed tmp/census-20261116f`
(BASELINE_REV = the head consumed by routing-20261116e; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ed-candidate`
sha256 `d820683ea76b2229f84a627bfb872a73eac4be6ee502cb53243349a1b760e42b`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116f/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116e's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
