# M0146-0014 — S9 census re-check at b2c66c076 (2026-10-14)

`b2c66c076` (M0122-0009 S7: `pg_current_wal_*lsn` builtins and their `pg_lsn` arm in the planner's `exprType`)
changed `internal/executor` and `internal/optimizer/planner.go`, which S9(1)'s path list covers. This is a fresh
capture of all three corpora with `scripts/jointree-parity-capture.sh` (`diff-*.txt`), with first-divergence
records from `scripts/pg-plan-first-divergence.py`.

| corpus | match | records vs `routing-20261014g` |
|---|---|---|
| TPC-DS SF0.25 | 81 / 99 | identical |
| TPC-DS SF1 | 66 / 99 | identical |
| TPC-H | 14 / 22 | identical |

No task is filed: **exhausted at `b2c66c076`**. Item 10 is live.
