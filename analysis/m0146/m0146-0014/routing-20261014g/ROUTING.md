# M0146-0014 — S9 census re-check at 91842230a (2026-10-14)

`91842230a` (the WAL summarizer's activity row and `pg_stat_io` reads) changed `internal/executor/pgstat_io.go` and
`context.go`, which S9(1)'s path list covers. This is a fresh capture of all three corpora with
`scripts/jointree-parity-capture.sh` (`diff-*.txt`), with first-divergence records from
`scripts/pg-plan-first-divergence.py`.

| corpus | match | records vs `routing-20261014f` |
|---|---|---|
| TPC-DS SF0.25 | 81 / 99 | identical |
| TPC-DS SF1 | 66 / 99 | identical |
| TPC-H | 14 / 22 | identical |

No task is filed: **exhausted at `91842230a`**. Item 10 is live.
