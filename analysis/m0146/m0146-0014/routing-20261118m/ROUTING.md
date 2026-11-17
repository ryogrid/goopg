# M0146-0014 — census at d22373e57 (2026-11-18): exhausted tree=7e0046e30f01f8f7de5d9e9c34b07a170d6168be

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118l, reason=plan-relevant-change)
— code commits `848e4386d` (M0149-0001h1a33: reg values print as names in concat/format/||) and `d22373e57` (M0149-0001h1a34 slice 1: pg_class.reltype for system relations).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `d22373e57`.

Capture: `BASELINE_REV=6462c0800 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hm tmp/census-20261118m`
(BASELINE_REV = the head consumed by routing-20261118l; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hm-candidate`
sha256 `ce96cc81c64a4a0437dc5227df4276879899c96ae20037f41e7308e4ef2b6c06`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118m/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118l's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
