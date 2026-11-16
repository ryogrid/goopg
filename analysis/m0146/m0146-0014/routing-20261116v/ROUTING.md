# M0146-0014 — census at cce5f7043 (2026-11-16): exhausted tree=488e2a91457c1b1821e36dd69940e25e15d12de5

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116u, reason=plan-relevant-change)
— code commit `3238245ff` (M0149-0001g slice 20: text-search bootstrap catalogs; docs follow-up cce5f7043).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `cce5f7043`.

Capture: `BASELINE_REV=ebc5f271b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9et tmp/census-20261116v`
(BASELINE_REV = the head consumed by routing-20261116u; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9et-candidate`
sha256 `1d33ff45a31b0677aa33796d95d9c4fcc6e937e78a52916d4725e12b7548c618`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116v/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116u's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
