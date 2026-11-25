# M0146-0014 — census at 47d736afb (2026-11-15): exhausted tree=8bda5121a04591a37e7fe41c8fbbdc7dcc912ffc

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115g, reason=plan-relevant-change)
— code commit `47d736afb` (M0148-0001c3a10 slice 4: index-only scans take an equality prefix plus a trailing NullTest).
The trigger files are
`internal/optimizer/createplanindex.go`, `internal/optimizer/pathindexonly.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `47d736afb`.

Capture: `BASELINE_REV=e8566acff CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dl tmp/census-20261115h`
(BASELINE_REV = the head consumed by routing-20261115g; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dl-candidate`
sha256 `32bcd8056d906f3fcf0b617d58900fb12a4fa5165e7eb3182ceba00a2e204398`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115h/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115g's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
