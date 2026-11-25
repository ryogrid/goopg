# M0146-0014 — census at 68bb50321 (2026-11-17): exhausted tree=57680e28b31500bd355b05e4ab029992264639f3

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117t, reason=plan-relevant-change)
— code commit `68bb50321` (M0149-0001h1a8: SCHEMAS default ACLs; schema OID minting fix).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `68bb50321`.

Capture: `BASELINE_REV=ec9c61e00 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gu tmp/census-20261117u`
(BASELINE_REV = the head consumed by routing-20261117t; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gu-candidate`
sha256 `92ba05833daef250f9127ad63134b914d8b6af6c0e43312d11ea0746cf72d648`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117u/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117t's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
