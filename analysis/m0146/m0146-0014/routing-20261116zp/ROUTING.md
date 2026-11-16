# M0146-0014 — census at 257ba97d1 (2026-11-16): exhausted tree=d9b6e97d42a960fee8fa2187e825f56febee5cc6

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116zo, reason=plan-relevant-change)
— code commit `257ba97d1` (M0149-0001k: fixed-length geometric/tid storage behind a cluster capability).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `257ba97d1`.

Capture: `BASELINE_REV=873e07d0e CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9fp tmp/census-20261116zp`
(BASELINE_REV = the head consumed by routing-20261116zo; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9fp-candidate`
sha256 `cd39fee0cc855d8fe851c1f13ca63dac404fc48752738fa422e8968f8c4aef31`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116zp/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116zo's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
