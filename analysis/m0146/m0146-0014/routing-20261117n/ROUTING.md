# M0146-0014 — census at 02a20cfae (2026-11-17): exhausted tree=0581e0018c001000421f6d7ef82f5a903c5697bf

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117m, reason=plan-relevant-change)
— code commit `02a20cfae` (M0149-0001h1a: GRANT / REVOKE ON LARGE OBJECT, ALTER LARGE OBJECT … OWNER TO).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `02a20cfae`.

Capture: `BASELINE_REV=409bfdab1 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gn tmp/census-20261117n`
(BASELINE_REV = the head consumed by routing-20261117m; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gn-candidate`
sha256 `511dcba4c135ed64596fe920225b19c438bda29ecd2d49985cdf8a67a8b3be92`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117n/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117m's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
