# M0146-0014 — census at 528b72f30 (2026-11-15): exhausted tree=c4d62a2600307c0d47fc9a801f06792064702836

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115i, reason=plan-relevant-change)
— code commit `528b72f30` (M0148-0001c3a10 slice 6: Merge Append for unrestricted ordered partitioned tables; routed ON CONFLICT arbiter fix).
The trigger files are
`internal/optimizer/partitionwisejoin.go`, `internal/executor/operators_upsert.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `528b72f30`.

Capture: `BASELINE_REV=8329da6c9 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dn tmp/census-20261115j`
(BASELINE_REV = the head consumed by routing-20261115i; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dn-candidate`
sha256 `ef4731208e97baf43335bb0ad6c21ddb0161bc6a8131bdaddc1d1fe49bbed2cf`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115j/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115i's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
