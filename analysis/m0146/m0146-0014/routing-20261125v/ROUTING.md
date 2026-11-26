# M0146-0014 — census at 01e26dd1d (2026-11-19): exhausted tree=0168dd162a6b1c384a410860f0db276d4c0592f8

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261125u, reason=plan-relevant-change)
— code commit `01e26dd1d` (after 01e26dd1d (M0149-0049b FULL HOT old tuple)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `01e26dd1d`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iv tmp/census-20261125v`
(BASELINE_REV = the head consumed by routing-20261125u; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iv-candidate`
sha256 `05f2ae2bd80908ceadde12cdef01999f363c09810072a3e7d8227a6d24119e97`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261125v/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261125u's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
