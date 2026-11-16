# M0146-0014 — census at 873e07d0e (2026-11-16): exhausted tree=77764d0bf87dc1b177a97579a144b05dea80be01

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116zn, reason=plan-relevant-change)
— code commit `873e07d0e` (M0149-0001j: typed date-time stavalues behind a round-trip guard).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `873e07d0e`.

Capture: `BASELINE_REV=06cfa776f CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9fo tmp/census-20261116zo`
(BASELINE_REV = the head consumed by routing-20261116zn; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9fo-candidate`
sha256 `64c78e53b207e02df23b86dcab236490d469ba9b4690c0f6cce1407294230b67`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116zo/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116zn's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
