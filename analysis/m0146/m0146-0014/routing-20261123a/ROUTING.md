# M0146-0014 — census at d21e2aa87 (2026-11-19): exhausted tree=272c1663b0fcf023cc008cd67852a2ab50c0af65

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119z, reason=plan-relevant-change)
— code commit `d21e2aa87` (M0149-0001h1a78c: string_to_array).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `d21e2aa87`.

Capture: `BASELINE_REV=3e8a938e3 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ia tmp/census-20261123a`
(BASELINE_REV = the head consumed by routing-20261119z; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ia-candidate`
sha256 `d81e2ac7565c87148fc3a9fa9ef100b1042294f67adfc8cd45a43bc23e50460e`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123a/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119z's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
