# M0146-0014 — census at 61c2f1857 (2026-11-14): exhausted tree=cd2fcc7803062cb8613475557644e6e917bde5a0

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114q, reason=plan-relevant-change)
— code commit `61c2f1857` (M0148-0001c3a6 item (i) slice 1: cross-type integer array literals merge into the index probe).
The trigger files include `internal/optimizer/pathbitmapor.go`, `internal/optimizer/pathindexrestrict.go` and
`internal/executor/operators_explain.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `0e930b554`.

Capture: `BASELINE_REV=c71667248 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9cw tmp/census-20261114r`
(BASELINE_REV = the head consumed by routing-20261114q; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9cw-candidate`
sha256 `ac7ccecd926e988c0cc7f5eae7fe9d867daaab076976cd0e552771cbe03fc9d5`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114r/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114q's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
