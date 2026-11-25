# M0146-0014 — census at 7fd3f816b (2026-11-19): exhausted tree=760188b4efd4ee497ae908f79c590d53a3c8df7f

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119d, reason=plan-relevant-change)
— code commit `7fd3f816b` (M0149-0001h1a67: date and time array input).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `7fd3f816b`.

Capture: `BASELINE_REV=c578939b9 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ie tmp/census-20261119e`
(BASELINE_REV = the head consumed by routing-20261119d; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ie-candidate`
sha256 `5f0e2ffe0b4cd7d896beb06ed40eb9baaac9890845b77b42128aee4ecab95add`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119e/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119d's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
