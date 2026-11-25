# M0146-0014 — census at e7899edfd (2026-11-18): exhausted tree=b5374bb907da3ed1872f3552405f703d372b94f3

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118w, reason=plan-relevant-change)
— code commit `e7899edfd` (M0149-0001h1a53: to_char for numbers and intervals).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `e7899edfd`.

Capture: `BASELINE_REV=f0c4cfc73 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hx tmp/census-20261118x`
(BASELINE_REV = the head consumed by routing-20261118w; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hx-candidate`
sha256 `a39dd3c7fa22773347433fb4135a8059c7569c6c0545f912e0b837aaf38ae22c`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118x/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118w's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
