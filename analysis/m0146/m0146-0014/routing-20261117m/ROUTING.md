# M0146-0014 — census at 409bfdab1 (2026-11-17): exhausted tree=98800f6aa02df46f7413c82b4b8d604e5ceaca36

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117l, reason=plan-relevant-change)
— code commit `409bfdab1` (M0149-0001h1a1: bit-string literals, operators and casts).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `409bfdab1`.

Capture: `BASELINE_REV=93ca7d4cb CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gm tmp/census-20261117m`
(BASELINE_REV = the head consumed by routing-20261117l; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gm-candidate`
sha256 `f85f6d3d3352633f0fc020d38433f0af83e08e26e7a7223f362c87ac6fa3d78f`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117m/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117l's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
