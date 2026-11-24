# M0146-0014 — census at c64a6c641 (2026-11-19): exhausted tree=cc33f707f795cb3d742160144662e96dc7a2ef74

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124h, reason=plan-relevant-change)
— code commit `c64a6c641` (M0149-0016: catalog btree, FSM guard, BuiltinSRFScan, ExtendSUBTRANS).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `c64a6c641`.

Capture: `BASELINE_REV=27e95f08b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ii tmp/census-20261124i`
(BASELINE_REV = the head consumed by routing-20261124h; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ii-candidate`
sha256 `c17aadd5a283e0ea44283ce6939fd303b2a405db4104d61809451253c6253d8b`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124i/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124h's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
