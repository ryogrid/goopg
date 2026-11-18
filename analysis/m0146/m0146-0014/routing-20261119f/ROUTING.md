# M0146-0014 — census at 901476aef (2026-11-19): exhausted tree=2e3c99db2bfd6acefaca6a27e47513fbd1114b41

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119e, reason=plan-relevant-change)
— code commit `901476aef` (M0149-0001h1a56: date and timestamp input parser).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `901476aef`.

Capture: `BASELINE_REV=7fd3f816b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9if tmp/census-20261119f`
(BASELINE_REV = the head consumed by routing-20261119e; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9if-candidate`
sha256 `e20387fc2671a429fe2c018b1877d97bf4a781c52341e3d9fc655debbd448226`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119f/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119e's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
