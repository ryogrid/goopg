# M0146-0014 — census at 0674306c7 (2026-11-14): exhausted tree=c87c125e5c2617188d3f7eeffb4213e36700248a

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114r, reason=plan-relevant-change)
— code commit `0674306c7` (M0148-0001c3a6 item (i) slice 2: cross-type integer literals are index quals and print as their own Const).
The trigger files include `internal/optimizer/pathbitmap.go`, `internal/optimizer/selectivity.go` and
`internal/executor/operators_explain.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `4c13945ea`.

Capture: `BASELINE_REV=0e930b554 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9cx tmp/census-20261114s`
(BASELINE_REV = the head consumed by routing-20261114r; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9cx-candidate`
sha256 `41e11d6f5f95d3e218e6fa7a71ab5e2d70508c6e1974cefaa9d122ec464346a1`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114s/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114r's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
