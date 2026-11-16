# M0146-0014 — census at 3e7e3fba2 (2026-11-16): exhausted tree=6013d74042b7811034b96fa65d9e99b30c391806

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116q, reason=plan-relevant-change)
— code commit `3e7e3fba2` (M0149-0001g slice 16: system_functions.sql pg_proc overrides).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `3e7e3fba2`.

Capture: `BASELINE_REV=7ea9a1928 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ep tmp/census-20261116r`
(BASELINE_REV = the head consumed by routing-20261116q; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ep-candidate`
sha256 `939d3b22fc5dc50673e1e306b6958d6aa20c70be117e84e52189bf6dcca2e40b`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116r/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116q's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
