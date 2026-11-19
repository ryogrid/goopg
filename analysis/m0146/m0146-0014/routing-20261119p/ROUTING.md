# M0146-0014 — census at 3d80966cf (2026-11-19): exhausted tree=24388672784cb705f1d5e8aade47c1c139601811

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119o, reason=plan-relevant-change)
— code commit `3d80966cf` (M0149-0001h1a69: interval input accepts ago).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `3d80966cf`.

Capture: `BASELINE_REV=ce55ca479 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ip tmp/census-20261119p`
(BASELINE_REV = the head consumed by routing-20261119o; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ip-candidate`
sha256 `6c42ad5204edcfff8c000f31aa3081d9886fb0ec3fe29c32c93fcd6c40dc2b22`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119p/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119o's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
