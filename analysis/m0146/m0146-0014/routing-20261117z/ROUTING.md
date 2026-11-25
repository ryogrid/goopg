# M0146-0014 — census at 85b002b2a (2026-11-17): exhausted tree=bd19f3a8ab24441862363108303e4c563565bbbd

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117y, reason=plan-relevant-change)
— code commit `85b002b2a` (M0149-0001h1a16: pg_class.relam follows RELKIND_HAS_TABLE_AM).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `85b002b2a`.

Capture: `BASELINE_REV=bfba991eb CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gz tmp/census-20261117z`
(BASELINE_REV = the head consumed by routing-20261117y; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gz-candidate`
sha256 `13d4f9ab92e0df231a2783d2feefaabab60debf3f0f5067c754a6eec1e176e59`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117z/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117y's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
