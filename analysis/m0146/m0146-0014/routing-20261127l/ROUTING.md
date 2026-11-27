# M0146-0014 — census at 61395bf9d (2026-11-19): exhausted tree=241e44bb53d9e385e5f2695a258f9b7277ae3e3b

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261127k, reason=plan-relevant-change)
— code commit `61395bf9d` (after 61395bf9d (M0149-0062 nodeReferencesOuter Join case)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `61395bf9d`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9il tmp/census-20261127l`
(BASELINE_REV = the head consumed by routing-20261127k; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9il-candidate`
sha256 `bab49cdc1ca2d203cd7b4f0b995cf0bbe5cf534aafb183a3ae00cfbb46007781`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261127l/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261127k's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
