# M0146-0014 — census at 7ea9a1928 (2026-11-16): exhausted tree=98dc0e9224bbfeab80bed7419fd609e5a6cc4263

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116p, reason=plan-relevant-change)
— code commit `7ea9a1928` (M0149-0001g slice 15: pg_proc scalar attributes from pg_proc.dat).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `7ea9a1928`.

Capture: `BASELINE_REV=a37f5016f CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9eo tmp/census-20261116q`
(BASELINE_REV = the head consumed by routing-20261116p; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9eo-candidate`
sha256 `83d2dc9bb5960b1d84a756fcc194ebc482153654ea65f72a372b497e79891e04`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116q/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116p's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
