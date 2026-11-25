# M0146-0014 — census at 6ffd8f664 (2026-11-16): exhausted tree=95c552bce6249ea48abe25a17e54bbfb171e9d45

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116h, reason=plan-relevant-change)
— code commit `6ffd8f664` (M0149-0001g slice 7: pg_opclass/pg_opfamily rows match PG 18.3 postgres.bki).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `6ffd8f664`.

Capture: `BASELINE_REV=d041357f2 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9eg tmp/census-20261116i`
(BASELINE_REV = the head consumed by routing-20261116h; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9eg-candidate`
sha256 `b9e39edae484519f99ba578de7f504099cf0989acbaebaee08ab43bcbe901dce`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116i/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116h's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
