# M0146-0014 — census at 5aa7625f0 (2026-11-19): exhausted tree=1ade2a3d31b3683db212f3041093994892d760dd

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123a, reason=plan-relevant-change)
— code commit `5aa7625f0` (M0149-0001h1a78d: VERBOSE ProjectSet targetlists).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `5aa7625f0`.

Capture: `BASELINE_REV=d21e2aa87 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ib tmp/census-20261123b`
(BASELINE_REV = the head consumed by routing-20261123a; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ib-candidate`
sha256 `89e7d4fa996940f798617c19c7d182ca5836a78c4ec17a5bbc82022ffcd4c4a0`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123b/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123a's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
