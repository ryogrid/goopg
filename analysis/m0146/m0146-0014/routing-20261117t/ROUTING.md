# M0146-0014 — census at ec9c61e00 (2026-11-17): exhausted tree=779f30acbcdaafafc069b43b23072fbd950dcfdf

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117s, reason=plan-relevant-change)
— code commit `ec9c61e00` (M0149-0001h1a7: default ACLs applied at CREATE TABLE / VIEW / SEQUENCE).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `ec9c61e00`.

Capture: `BASELINE_REV=a58f714ee CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gt tmp/census-20261117t`
(BASELINE_REV = the head consumed by routing-20261117s; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gt-candidate`
sha256 `4a8c70c1e2410d205b2f587249fb9b0fccb5bcbf624cabf73666fc4e9f23a1bd`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117t/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117s's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
