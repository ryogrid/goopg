# M0146-0014 — census at fff19ea2c (2026-11-19): exhausted tree=bd95b7200ec185d42afc2adf33eabac707d42f08

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124z, reason=plan-relevant-change)
— code commit `fff19ea2c` (SRF in a single INSERT VALUES row (M0149-0023c)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `fff19ea2c`.

Capture: `BASELINE_REV=81b57146b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ia tmp/census-20261125a`
(BASELINE_REV = the head consumed by routing-20261124z; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ia-candidate`
sha256 `fe9408623cd5f37e71d0be7fb808412cbb3653634f61883eba410e204fa9a78a`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261125a/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124z's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
