# M0146-0014 — census at 081b8fea0 (2026-11-14): exhausted tree=000c1b605448248f573c71955debcb9109a53738

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114t, reason=plan-relevant-change)
— code commit `081b8fea0` (M0148-0001c3a6 item (i) slice 4: every range bound is an index clause).
The trigger files include `internal/optimizer/pathindexrestrict.go`, `internal/optimizer/createplanindex.go` and
`internal/executor/operators_explain.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `9fde11c1f`.

Capture: `BASELINE_REV=f3f880c38 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9cz tmp/census-20261114u`
(BASELINE_REV = the head consumed by routing-20261114t; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9cz-candidate`
sha256 `214a122f9cb1b035e253a3ed7f7bb9715aff4a887b27ec5841cfe886d7bbc9ff`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114u/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114t's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
