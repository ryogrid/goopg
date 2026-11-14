# M0146-0014 — census at a6725a9d1 (2026-11-14): exhausted tree=3fe6200bb254255dc114a3b7d97dfc80d4d33d50

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114p, reason=plan-relevant-change)
— code commit `a6725a9d1` (M0148-0001c3a5 item (a) slice 6: a one-leaf partitioned table plans its leaf as the appendrel's only member).
The trigger files are `internal/optimizer/planner.go` and
`internal/optimizer/cardinality.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `c71667248`.

Capture: `BASELINE_REV=2d2aee32a CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9cv tmp/census-20261114q`
(BASELINE_REV = the head consumed by routing-20261114p; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9cv-candidate`
sha256 `d580f1b333043cc1dc4e9b2b7f7490f58954d379f9e86e2c250179d09ff92e7a`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114q/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114p's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
