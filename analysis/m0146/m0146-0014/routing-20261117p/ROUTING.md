# M0146-0014 — census at d2b26da7e (2026-11-17): exhausted tree=01a248cf3b15c1d3f52c43b03949a34a31ecd87a

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117o, reason=plan-relevant-change)
— code commit `d2b26da7e` (M0149-0001h1a: fast-path FunctionCall protocol, pg_catalog-qualified call typing, lo_get size limit).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `d2b26da7e`.

Capture: `BASELINE_REV=b58d7ac25 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gp tmp/census-20261117p`
(BASELINE_REV = the head consumed by routing-20261117o; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gp-candidate`
sha256 `95ff76973d9226a18d26119bdfaa451a14df962c3479acd45ea24768e00dba92`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117p/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117o's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
