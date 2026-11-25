# M0146-0014 — census at 7283038a9 (2026-11-18): exhausted tree=bd308e02d0170a1ca0be8a90d9a4ee68e96e98f5

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118h, reason=plan-relevant-change)
— code commit `7283038a9` (M0149-0001h1a28: sub-partitioned partitions are relkind p; index relowner follows the table).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `7283038a9`.

Capture: `BASELINE_REV=fc9554b07 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hi tmp/census-20261118i`
(BASELINE_REV = the head consumed by routing-20261118h; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hi-candidate`
sha256 `c9dede20531def963edbc55a003d0f367539dd744b802045276672c56712f5f1`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118i/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118h's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
