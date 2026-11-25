# M0146-0014 — census at 99e42ad84 (2026-11-15): exhausted tree=ba013efc600c08fc6d9f063e94ebd3fe7517d058

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115l, reason=plan-relevant-change)
— code commit `99e42ad84` (M0148-0001c3a10 slice 9: unrestricted appendrel seed priced as cost_append).
The trigger files are
`internal/optimizer/joinsearch.go`, `internal/optimizer/partitionwisejoin.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `99e42ad84`.

Capture: `BASELINE_REV=d23bc5ee2 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dq tmp/census-20261115m`
(BASELINE_REV = the head consumed by routing-20261115l; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dq-candidate`
sha256 `3c6b55d19e0eb28a3da682306bd2df77e4e1e59568007046023efced6c342191`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115m/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115l's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
