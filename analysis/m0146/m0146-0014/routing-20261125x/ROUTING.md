# M0146-0014 — census at aebb78184 (2026-11-19): exhausted tree=6246e691ace102a803db730d489f3ce0309aa03d

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261125w, reason=plan-relevant-change)
— code commit `aebb78184` (after aebb78184 (M0149-0050c subscriber binary mode)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `aebb78184`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ix tmp/census-20261125x`
(BASELINE_REV = the head consumed by routing-20261125w; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ix-candidate`
sha256 `735180628757e5bd316c98043423106fe802579c5d65c3bb72dc2c786c829cb5`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261125x/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261125w's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
