# M0146-0014 — census at fe626d939 (2026-11-19): exhausted tree=97fbf6af0c601456ed0fcec99d9dca46e95eca9c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123b, reason=plan-relevant-change)
— code commit `fe626d939` (M0149-0001h1a78f: string_to_table).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `fe626d939`.

Capture: `BASELINE_REV=5aa7625f0 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ic tmp/census-20261123c`
(BASELINE_REV = the head consumed by routing-20261123b; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ic-candidate`
sha256 `2a228722f0dbbc73783174a846c56f0adab2333de49bb76fe3d73902284d8634`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123c/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123b's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
