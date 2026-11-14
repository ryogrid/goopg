# M0146-0014 — census at 9394bbc7b (2026-11-14): exhausted tree=364b9e07eb0a0530cd6b8fda8e739fd58c7c52e8

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114h, reason=plan-relevant-change)
— code commit `9394bbc7b` (M0148-0001c3a4 item (e): partial indexes proved by predicate_implied_by over the restriction).
The trigger files include `internal/optimizer/pathbitmap.go`, `pathbitmapor.go`, `planner.go` and
`internal/optimizer/createplanbitmap.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `73eb342b2`.

Capture: `BASELINE_REV=f7e010c4b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9cn tmp/census-20261114i`
(BASELINE_REV = the head consumed by routing-20261114h; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9cn-candidate`
sha256 `4a171ba7c69500c81b4876d7b97517b01e88a5b59881324897dea98c7ef2164e`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114i/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114h's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
