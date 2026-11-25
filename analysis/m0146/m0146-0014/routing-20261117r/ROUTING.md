# M0146-0014 — census at 100a4033c (2026-11-17): exhausted tree=43aeb6672664420d68b97f4275b52451ca7d4487

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117q, reason=plan-relevant-change)
— code commit `100a4033c` (M0149-0001h1a5: pg_roles view, PG-shaped bootstrap pg_authid rows).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `100a4033c`.

Capture: `BASELINE_REV=24f186f28 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gr tmp/census-20261117r`
(BASELINE_REV = the head consumed by routing-20261117q; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gr-candidate`
sha256 `376a706448995188b64f80841352deb9f53b3d4d5f401b2b23f83709ddae8014`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117r/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117q's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
