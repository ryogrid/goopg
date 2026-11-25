# M0146-0014 — census at 7321485a7 (2026-11-14): exhausted tree=c6af646fe072d0273ecda3711db36686a8ff78da

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114j, reason=plan-relevant-change)
— code commit `7321485a7` (M0148-0001c3a5 item (d) slice 2: simplify_boolean_equality, boolvarsel / booltestsel defaults, bare-column quals printed bare).
The trigger files include `internal/optimizer/foldconst.go`, `selectivity.go` and
`internal/executor/operators_explain.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `3c1c7a34c`.

Capture: `BASELINE_REV=13ed065fb CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9cp tmp/census-20261114k`
(BASELINE_REV = the head consumed by routing-20261114j; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9cp-candidate`
sha256 `24e1f5a3b3738fbc9c4f05bf1db39c9bb4b0b9750efd956a2f53c8d6355da0b8`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114k/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114j's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
