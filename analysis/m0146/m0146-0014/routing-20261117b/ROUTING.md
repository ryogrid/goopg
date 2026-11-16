# M0146-0014 — census at 3192ad986 (2026-11-17): exhausted tree=b7f45823ccb3a56ed996990addfdd025ce394013

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117a, reason=plan-relevant-change)
— code commit `3192ad986` (M0149-0001k3: geometric/tid array elements; box typdelim).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `3192ad986`.

Capture: `BASELINE_REV=38324a69b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gb tmp/census-20261117b`
(BASELINE_REV = the head consumed by routing-20261117a; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gb-candidate`
sha256 `8d1b971cf48ba986c80b720ef4df5d99df69aabb532f00a4463368d1ecf87648`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117b/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117a's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
