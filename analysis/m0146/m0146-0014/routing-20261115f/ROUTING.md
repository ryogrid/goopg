# M0146-0014 — census at d0e22ef1a (2026-11-15): exhausted tree=42210f730f70d841a8c15212247992c26bd63e3c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115e, reason=plan-relevant-change)
— code commit `d0e22ef1a` (M0148-0001c3a10 slice 2: index-only probes behind an equality prefix carry the index ordering).
The trigger file is
`internal/optimizer/pathindexonly.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `5d57b11f4`.

Capture: `BASELINE_REV=eefb34976 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dj tmp/census-20261115f`
(BASELINE_REV = the head consumed by routing-20261115e; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dj-candidate`
sha256 `75292640f7a0e8dbb56247a833b593044f021e7877b36debc4aa69249b3c51dc`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115f/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115e's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
