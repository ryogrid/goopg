# M0146-0014 — census at 663044bae (2026-11-16): exhausted tree=05c6af7f2327983237875df0e94043f42cc3bbcb

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116zf, reason=plan-relevant-change)
— code commit `663044bae` (M0149-0001g slice 31: initdb pg_depend rows).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `663044bae`.

Capture: `BASELINE_REV=fcd8aa879 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9fe tmp/census-20261116zg`
(BASELINE_REV = the head consumed by routing-20261116zf; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9fe-candidate`
sha256 `bb0cdce7c8741b7723fcce5135f18f1a6cbfd7bfa32b73615ca7bcc45351f15b`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116zg/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116zf's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
