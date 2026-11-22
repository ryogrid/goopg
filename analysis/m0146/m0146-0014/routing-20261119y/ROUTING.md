# M0146-0014 — census at fe6c96be5 (2026-11-19): exhausted tree=0820794a52f923f062e9b48c370aef996a90b69e

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119x, reason=plan-relevant-change)
— code commit `fe6c96be5` (M0149-0001h1a78a: a SELECT-list ProjectSet carries PG's estimates).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `fe6c96be5`.

Capture: `BASELINE_REV=f62ac6468 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iy tmp/census-20261119y`
(BASELINE_REV = the head consumed by routing-20261119x; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iy-candidate`
sha256 `260539727938a92be323675ea26aac0c1d09dd9c04598724c90973cbf242c17e`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119y/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119x's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
