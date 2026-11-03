# M0146-0014 — census at 0c056f9df (2026-11-03, run-2 loop #44): exhausted tree=af1f63b7a49f8bea76f74bbd2fad807323ef8316

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261103f, reason=plan-relevant-change)
— code commit `0c056f9df` (M0148-0001c3: partial-index predicate maintenance, NullTest predicate
proof, predicate-implied quals dropped; `internal/executor/partial_index_maintain.go`,
`internal/executor/operators_storage.go`, `internal/optimizer/partial_index_implied_quals.go`,
`internal/optimizer/predicate_implication.go`, ...). Its own fire-set gate measured `fires=none` on all three
corpora. `git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built
its engines, hence `tree=` above (HEAD's tree). Capture repo head `1aac37760` (records on top of the
code commit).

Capture: `BASELINE_REV=c7f3bb41d CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9am tmp/census-20261103g`
(BASELINE_REV = the head consumed by routing-20261103f; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9am-candidate`
sha256 `5ee09edbbe7bcb5c0fc282a1a99eedef33dcf58b39ca68636408466111bb1d02`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261103g/flat` (exit 0):

- `tpch IDENTICAL new=5175c597d5c008062df3cb56dc0f007e5eb92d2bb2019caf9d988cbb384c0f36 base=routing-20261102f/s9o-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261102f/s9o-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261102f/s9o-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
