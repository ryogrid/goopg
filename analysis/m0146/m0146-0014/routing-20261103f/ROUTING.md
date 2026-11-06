# M0146-0014 — census at a4f88b4aa (2026-11-03, run-2 loop #43): exhausted tree=b030c09e6da8a0dc2920bdf0b7ab2fab8caea43c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261103e, reason=plan-relevant-change)
— code commit `a4f88b4aa` (M0148-0001j: ordered aggregates never hashed, presorted aggregates
marked after the election, ORDER BY in the state-sharing key; `internal/optimizer/groupingpaths.go`,
`internal/optimizer/partialaggupper.go`, `internal/optimizer/plan.go`, `internal/optimizer/planner.go`,
`internal/executor/operators_join_agg.go`). Its own fire-set gate measured `fires=none` on all three
corpora. `git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built
its engines, hence `tree=` above (HEAD's tree). Capture repo head `c7f3bb41d` (records on top of the
code commit).

Capture: `BASELINE_REV=2daba032c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ak tmp/census-20261103f`
(BASELINE_REV = the head consumed by routing-20261103e; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ak-candidate`
sha256 `edf44748959679ffcf3517c1416f2803864cf33acb18a42eeb3634f775ad8460`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261103f/flat` (exit 0):

- `tpch IDENTICAL new=5175c597d5c008062df3cb56dc0f007e5eb92d2bb2019caf9d988cbb384c0f36 base=routing-20261102f/s9o-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261102f/s9o-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261102f/s9o-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
