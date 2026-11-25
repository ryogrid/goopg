# M0146-0014 — census at b6bfa4a48 (2026-11-19): exhausted tree=96f46c12bb0cbc3bc7a1365997496d1158645ac2

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123x, reason=plan-relevant-change)
— code commit `b6bfa4a48` (M0149-0010: init NextXID 752).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `b6bfa4a48`.

Capture: `BASELINE_REV=4ef5fe018 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iy tmp/census-20261123y`
(BASELINE_REV = the head consumed by routing-20261123x; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iy-candidate`
sha256 `211ce71c55555baa602fc496806de1e85082c42115ae737d32f1f4536844deea`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123y/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123x's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
