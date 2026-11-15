# M0146-0014 — census at d041357f2 (2026-11-16): exhausted tree=a6c01b0889e72f6487ad1b9d692ddde2d80acea8

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116g, reason=plan-relevant-change)
— code commit `d041357f2` (M0149-0001g slice 6: pg_ts_template indexes no longer overwritten by heap placeholders).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `d041357f2`.

Capture: `BASELINE_REV=dd09fff03 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ef tmp/census-20261116h`
(BASELINE_REV = the head consumed by routing-20261116g; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ef-candidate`
sha256 `5ece276ad1c4086b0bb59e28a48deb1298865dd70c7a3ed93997e30335456cd6`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116h/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116g's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
