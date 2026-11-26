# M0146-0014 — census at 8bb175099 (2026-11-19): exhausted tree=a97c629dd46e8a25d0ddea343117bf4fd6983c80

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261127b, reason=plan-relevant-change)
— code commit `8bb175099` (M0149-0057c/d publisher slot feedback + publish_via_partition_root).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `8bb175099`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ic tmp/census-20261127c`
(BASELINE_REV = the head consumed by routing-20261127b; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ic-candidate`
sha256 `f7229a1e32da6da1ccdd72121f141aad7d9ecce07b3d7b193de34eac5227bbbe`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261127c/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261127b's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
