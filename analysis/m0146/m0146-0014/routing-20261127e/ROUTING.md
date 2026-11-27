# M0146-0014 — census at 3317ec430 (2026-11-19): exhausted tree=49459d5cc4fe7b8c65d2e78c79796a1a2696bf28

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261127d, reason=plan-relevant-change)
— code commit `3317ec430` (M0149-0058a run_as_owner + SET/INHERIT-aware apply checks).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `3317ec430`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ie tmp/census-20261127e`
(BASELINE_REV = the head consumed by routing-20261127d; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ie-candidate`
sha256 `bb13be20c5c3fa3ec26cf368e9018a7f2708c51bbb14d9b4e15aab6adb0003e0`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261127e/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261127d's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
