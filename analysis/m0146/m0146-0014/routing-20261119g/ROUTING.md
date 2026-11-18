# M0146-0014 — census at 3394aa34b (2026-11-19): exhausted tree=8ebee01b466702ba7c07059203bc30484d699497

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119f, reason=plan-relevant-change)
— code commit `3394aa34b` (M0149-0001h1a55: typed literal error positions).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `3394aa34b`.

Capture: `BASELINE_REV=901476aef CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ig tmp/census-20261119g`
(BASELINE_REV = the head consumed by routing-20261119f; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ig-candidate`
sha256 `fd5dc2bcbbdd6f22aca5ab3ffbec4e335f4862c5dfd2de1b46ea7796f7521eee`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119g/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
