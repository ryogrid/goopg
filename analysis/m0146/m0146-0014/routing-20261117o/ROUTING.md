# M0146-0014 — census at b58d7ac25 (2026-11-17): exhausted tree=bd41f169f5c6d56b757d266eefaba5ebfb1864c5

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117n, reason=plan-relevant-change)
— code commit `b58d7ac25` (M0149-0001h1a: COMMENT ON LARGE OBJECT, PL/pgSQL exception-condition matching, loread holes).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `b58d7ac25`.

Capture: `BASELINE_REV=02a20cfae CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9go tmp/census-20261117o`
(BASELINE_REV = the head consumed by routing-20261117n; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9go-candidate`
sha256 `7faad3b36f4be2cfedec696a805be528dd03ba2508ee592dc9c8bec32708bde7`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117o/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117n's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
