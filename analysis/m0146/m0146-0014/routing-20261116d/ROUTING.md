# M0146-0014 — census at fbd466e3f (2026-11-16): exhausted tree=84a00153d18162bb5b0fca5787c3022cdbd0654f

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116c, reason=plan-relevant-change)
— code commit `fbd466e3f` (M0149-0001g slice 2: generic catalog-index maintenance + six missing catalog indexes).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `fbd466e3f`.

Capture: `BASELINE_REV=54c05f6a7 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9eb tmp/census-20261116d`
(BASELINE_REV = the head consumed by routing-20261116c; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9eb-candidate`
sha256 `24504dad34f6d51065463d479713beee52b952e7e60559a00d028ab401fd2a32`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116d/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116c's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
