# M0146-0014 — census at d4a5f0b30 (2026-11-03, run-2 loop #40): exhausted tree=c33160481d8cd88a4d746977f911d7906b6708e2

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261103b, reason=plan-relevant-change)
— code commits since its capture: `7dddb23c0` (M0148-0001h slice 3: session-scoped temp views) and
`d4a5f0b30` (M0148-0001i slice 1: user aggregates with a COMBINEFUNC split across Gather;
`internal/optimizer/parallel_agg.go`). Each commit's own fire-set gate measured `fires=none` on all
three corpora. `git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the
capture built its engines, hence `tree=` above (HEAD's tree). Capture repo head `1df1f5e50`
(records on top of `d4a5f0b30`).

Capture: `BASELINE_REV=a81b80971 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ae tmp/census-20261103c`
(BASELINE_REV = the head consumed by routing-20261103b; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ae-candidate`
sha256 `24697c1de4464d6af41e7798b408080bbad7b53fa049e3eba4013f42e82b4186`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261103c/flat` (exit 0):

- `tpch IDENTICAL new=5175c597d5c008062df3cb56dc0f007e5eb92d2bb2019caf9d988cbb384c0f36 base=routing-20261102f/s9o-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261102f/s9o-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261102f/s9o-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
