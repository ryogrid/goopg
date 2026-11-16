# M0146-0014 — census at 87c1ded5a (2026-11-16): exhausted tree=efc8cea9150d7c14bc4c09993943f5967c939925

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116w, reason=plan-relevant-change)
— code commit `87c1ded5a` (M0149-0001g slice 22: pg_init_privs rows and bootstrap schema nspacl).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `87c1ded5a`.

Capture: `BASELINE_REV=d5ed00ffe CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ev tmp/census-20261116x`
(BASELINE_REV = the head consumed by routing-20261116w; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ev-candidate`
sha256 `f14710eb8c39369da97672b54a4c3ff123c0fa732c1bd5efc865643ba0531024`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116x/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116w's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
