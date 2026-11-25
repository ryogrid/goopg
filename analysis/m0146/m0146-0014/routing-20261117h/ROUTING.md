# M0146-0014 — census at ea79c6c06 (2026-11-17): exhausted tree=476ebf884792ebf08f97bef8412e9688bc26d989

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117g, reason=plan-relevant-change)
— code commit `ea79c6c06` (M0149-0001h: has_database_privilege + template datacl (20cd466f6)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `ea79c6c06`.

Capture: `BASELINE_REV=920254c9b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gh tmp/census-20261117h`
(BASELINE_REV = the head consumed by routing-20261117g; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gh-candidate`
sha256 `33fab91c6bb241b85eeaaa2c1e503fdaabd935a00322516f3d757edaa87d439e`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117h/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117g's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
