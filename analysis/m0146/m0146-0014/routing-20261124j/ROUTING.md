# M0146-0014 — census at 7139c4ab9 (2026-11-19): exhausted tree=c8c837e33ce81689208ef867be1fe36b80f4b8e4

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124i, reason=plan-relevant-change)
— code commit `7139c4ab9` (M0149-0017 slice 1: wal_keep_size, PL/pgSQL SELECT exprs, pg_walfile_name).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `7139c4ab9`.

Capture: `BASELINE_REV=c64a6c641 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ij tmp/census-20261124j`
(BASELINE_REV = the head consumed by routing-20261124i; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ij-candidate`
sha256 `307aa40a57ef4d683bc3ade33d021753898526cf40a7f984010f81c5d82000e8`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124j/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124i's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
