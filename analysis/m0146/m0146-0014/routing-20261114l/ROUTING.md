# M0146-0014 — census at c5546dffd (2026-11-14): exhausted tree=3c33779b7be1eec4100e57e5a395ad61115b2cd3

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114k, reason=plan-relevant-change)
— code commit `c5546dffd` (M0148-0001c3a5 item (a) slice 1: partition-ordered plain Append over per-partition index scans).
The trigger files are `internal/optimizer/orderedpartitionappend.go` (new) and
`internal/optimizer/upperordered.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `e4787ad9e`.

Capture: `BASELINE_REV=3c1c7a34c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9cq tmp/census-20261114l`
(BASELINE_REV = the head consumed by routing-20261114k; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9cq-candidate`
sha256 `12c2b3bdea1ecd1d6adbccf1bfca5676dc93f448d4749973583cacd49184058b`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114l/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114k's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
