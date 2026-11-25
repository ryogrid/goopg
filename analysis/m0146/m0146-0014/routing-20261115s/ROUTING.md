# M0146-0014 — census at b229fa406 (2026-11-15): exhausted tree=c85a0f31264088636a5c19276439037c5763942d

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115r, reason=plan-relevant-change)
— code commits `64cc168b6` (M0149-0001b: tapnode test harness) and `b229fa406` (M0149-0001c: initdb
bootstraps pg_shdepend, pg_shdescription and the shared TOAST pairs).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `b229fa406`.

Capture: `BASELINE_REV=2ab2cbe4c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dw tmp/census-20261115s`
(BASELINE_REV = the head consumed by routing-20261115r; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dw-candidate`
sha256 `7bf8ba5ae717855e96ef4d4c7866f3cfcdb8bf0053e65702d6a8ab8a47471bc4`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115s/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115r's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
