# M0146-0014 — census at 5c97624f6 (2026-11-16): exhausted tree=7fc4d73af924e56aaa39fb8ca7e609079802471b

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116n, reason=plan-relevant-change)
— code commit `5c97624f6` (M0149-0001g slice 13: pg_proc.proacl, _aclitem 8-byte alignment).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `5c97624f6`.

Capture: `BASELINE_REV=bc65bd192 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9em tmp/census-20261116o`
(BASELINE_REV = the head consumed by routing-20261116n; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9em-candidate`
sha256 `b6960b5e3e0dbf3ff493e9336175cb7f170cf3d460d28e2c3f01320d74dbdfc3`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116o/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116n's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
