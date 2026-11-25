# M0146-0014 — census at 3167621c3 (2026-11-15): exhausted tree=3ecb534af09c3e8e2091bf218cb7a099abce2b04

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115f, reason=plan-relevant-change)
— code commit `3167621c3` (M0148-0001c3a10 slice 3: index-only child paths and the ordered Append over partitions).
The trigger file is
`internal/optimizer/partitionwisejoin.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `e8566acff`.

Capture: `BASELINE_REV=5d57b11f4 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dk tmp/census-20261115g`
(BASELINE_REV = the head consumed by routing-20261115f; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dk-candidate`
sha256 `0ca52eeb25c526a3aea3e99529d7a9e8b674215cf899b0fc16f8485210876944`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115g/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
