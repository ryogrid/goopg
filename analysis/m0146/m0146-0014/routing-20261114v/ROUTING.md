# M0146-0014 — census at 6c603dd22 (2026-11-14): exhausted tree=c543e0fd18a6d64ea963e8b917bb8cf26c6c9e34

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114u, reason=plan-relevant-change)
— code commit `6c603dd22` (M0148-0001c3a6 item (i) slice 5: `col IN (…) AND col = const` merges the equality into the array path).
The trigger files include `internal/optimizer/pathindexrestrict.go`, `internal/optimizer/pathindexonly.go` and
`internal/optimizer/pathbitmap.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `a7fd3bccf`.

Capture: `BASELINE_REV=9fde11c1f CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9da tmp/census-20261114v`
(BASELINE_REV = the head consumed by routing-20261114u; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9da-candidate`
sha256 `55de8a3570d39644b5bcec784e46078e8bca4255e8815822f94e9a976a14f55b`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114v/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114u's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
