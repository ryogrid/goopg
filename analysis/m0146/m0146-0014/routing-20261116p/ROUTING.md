# M0146-0014 — census at a37f5016f (2026-11-16): exhausted tree=22de2e55054f3894af9d04274f2802a8f262f066

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116o, reason=plan-relevant-change)
— code commit `a37f5016f` (M0149-0001g slice 14: PhysicalTypeAlign pinned to pg_type.dat, array element alignment).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `a37f5016f`.

Capture: `BASELINE_REV=5c97624f6 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9en tmp/census-20261116p`
(BASELINE_REV = the head consumed by routing-20261116o; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9en-candidate`
sha256 `86bbd22a78e2575269ecaac31831a05be5118a8cbc03d073298ce1414d0155a5`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116p/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116o's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
