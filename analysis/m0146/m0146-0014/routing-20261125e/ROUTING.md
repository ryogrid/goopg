# M0146-0014 — census at 464c233d6 (2026-11-19): exhausted tree=e6d5833d754e7d5480b1793c4cc45b0a70d25a3b

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261125d, reason=plan-relevant-change)
— code commit `464c233d6` (standby routine refresh from replayed pg_proc (M0149-0032a)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `464c233d6`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ie tmp/census-20261125e`
(BASELINE_REV = the head consumed by routing-20261125d; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ie-candidate`
sha256 `a8748b28c61efc86aa13129a77bb1f4cd0bb02b43549ed567f01aae4d4df8c3e`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261125e/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261125d's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
