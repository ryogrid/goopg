# M0146-0014 — census at 7f773aa4b (2026-11-12, run-2 loop #29): exhausted tree=df691c09b8099fbcbbbdd43c2cb5f92db25f17d2

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261112n, reason=plan-relevant-change)
— code commit `7f773aa4b` (M0148-0001c3a3: planner backward index paths).
The trigger files were `internal/optimizer/pathindexordered.go`, `pathindexrestrict.go`, `createplanindex.go` and `planner.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `e850227a6`.

Capture: `BASELINE_REV=ee781b38c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9bz tmp/census-20261112o`
(BASELINE_REV = the head consumed by routing-20261112n; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9bz-candidate`
sha256 `1b582bf9e1ff94c43deb7eebf73f205a7888869ce9eacbd9e3662a90dc363ecc`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261112o/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261112n's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
