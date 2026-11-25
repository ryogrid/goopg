# M0146-0014 — census at 746e797da (2026-11-19): exhausted tree=355e1cf52154df873565f7e1db2cb536f5d1412d

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124p, reason=plan-relevant-change)
— code commit `746e797da` (pg_replication_slots xmin NULL rendering (M0149-0077g)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `746e797da`.

Capture: `BASELINE_REV=94e24a6a6 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iq tmp/census-20261124q`
(BASELINE_REV = the head consumed by routing-20261124p; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iq-candidate`
sha256 `dbc584a17a30bff920518e4e79016f821509fe0969383c39f192948e6ca22970`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124q/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124p's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
