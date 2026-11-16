# M0146-0014 — census at f2068bdbf (2026-11-16): exhausted tree=df3f151109ec1b88f6aa3977e79cc6d4b6916a4c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116zj, reason=plan-relevant-change)
— code commit `f2068bdbf` (M0149-0001j: typed pg_statistic stavalues).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `f2068bdbf`.

Capture: `BASELINE_REV=136ba8262 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9fi tmp/census-20261116zk`
(BASELINE_REV = the head consumed by routing-20261116zj; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9fi-candidate`
sha256 `620b11ed66a607c3025643ab113148dc51e6ffd9fbf281655c2e72130796b4e6`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116zk/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116zj's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
