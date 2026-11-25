# M0146-0014 — census at 0b1c0660c (2026-11-19): exhausted tree=f8d8fbb5d996fa640e16ae6986bf1a46e1a079af

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123z, reason=plan-relevant-change)
— code commit `0b1c0660c` (M0149-0013: replay delay + pause).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `0b1c0660c`.

Capture: `BASELINE_REV=c9e7861d5 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ia tmp/census-20261124a`
(BASELINE_REV = the head consumed by routing-20261123z; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ia-candidate`
sha256 `1e9c8729a56ed7d39dc1ed6331a6d707ffe12dc0415afb0ba727c9b7bf1ea5aa`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124a/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123z's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
