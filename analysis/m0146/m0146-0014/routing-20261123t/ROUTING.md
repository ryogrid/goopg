# M0146-0014 — census at f9dc66d14 (2026-11-19): exhausted tree=fb5379503f0a70d256eee989959dff545e98ab48

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123s, reason=plan-relevant-change)
— code commit `f9dc66d14` (M0149-0007 slice 3: pg_stat_io persistence).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `f9dc66d14`.

Capture: `BASELINE_REV=eeb5120da CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9it tmp/census-20261123t`
(BASELINE_REV = the head consumed by routing-20261123s; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9it-candidate`
sha256 `6b051fa25c034ad0a4072cff65e423f74c9031b9ddf4c8ca9deb512cb1342ad6`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123t/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123s's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
