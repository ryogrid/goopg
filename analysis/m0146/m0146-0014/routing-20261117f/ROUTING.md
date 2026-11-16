# M0146-0014 — census at 4e4752446 (2026-11-17): exhausted tree=37bd130bd48724fc0392d2ed8d7341ca10d2a37f

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117e, reason=plan-relevant-change)
— code commit `4e4752446` (M0149-0001k5: typed money statistics, money-aware selectivity).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `4e4752446`.

Capture: `BASELINE_REV=3559f6894 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gf tmp/census-20261117f`
(BASELINE_REV = the head consumed by routing-20261117e; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gf-candidate`
sha256 `970d4b8de4dad209d4a87b543cf9c6506ecd76e1ccf9c2834fbcac00cf281248`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117f/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117e's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
