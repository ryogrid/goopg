# M0146-0014 — census at 36d08b8ea (2026-11-17): exhausted tree=9ccddde3640b7e09481a5d96542a6611f8e538d1

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117u, reason=plan-relevant-change)
— code commits `c2c408ac7` (aclitemin / makeaclitem), `d265c7e78` (quoted role identifiers), `ebdd9c9a9` (uuid input) and `36d08b8ea` (M0149-0001h1a12: regrolein, to_reg*, nested-cast column labels).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `36d08b8ea`.

Capture: `BASELINE_REV=68bb50321 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gv tmp/census-20261117v`
(BASELINE_REV = the head consumed by routing-20261117u; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gv-candidate`
sha256 `623c902661208b38f62e4f61636440c0e80b4e81dc3c877c2e6c7feb598401e7`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117v/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117u's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
