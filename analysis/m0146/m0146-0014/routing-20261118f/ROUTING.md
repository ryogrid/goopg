# M0146-0014 — census at eea364946 (2026-11-18): exhausted tree=61d5da7a465644de25cdac6cc433b7f21ac71f91

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118e, reason=plan-relevant-change)
— code commit `eea364946` (M0149-0001h1a23: pg_get_function_result / _arguments / _identity_arguments for built-in functions).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `eea364946`.

Capture: `BASELINE_REV=1cf76a7d0 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hf tmp/census-20261118f`
(BASELINE_REV = the head consumed by routing-20261118e; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hf-candidate`
sha256 `ffb7bfa0da9c7bae7f334dfcfe0e74fa092770db734d2e111ff864541c65ce88`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118f/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118e's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
