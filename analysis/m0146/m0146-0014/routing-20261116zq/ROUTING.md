# M0146-0014 — census at 7e0c6e98b (2026-11-16): exhausted tree=2d6d3e1ef787023ce13860d394228fad2b08c97c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116zp, reason=plan-relevant-change)
— code commit `7e0c6e98b` (M0149-0001k1: money as a real type with 8-byte Cash storage).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `7e0c6e98b`.

Capture: `BASELINE_REV=257ba97d1 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9fq tmp/census-20261116zq`
(BASELINE_REV = the head consumed by routing-20261116zp; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9fq-candidate`
sha256 `ba6f99ade2574d894488f37dda55ab685c94ecfc145ddaa070bd3abef38ac213`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116zq/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116zp's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
