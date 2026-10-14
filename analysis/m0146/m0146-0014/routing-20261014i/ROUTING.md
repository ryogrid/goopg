# M0146-0014 — S9 census re-check at 7dfe93df3 (2026-10-14)

`7dfe93df3` (M0122-0009 S6b: dropped relations' storage removed after the commit record) changed
`internal/executor`, which S9(1)'s path list covers. This is a fresh capture of all three corpora with
`scripts/jointree-parity-capture.sh` (`diff-*.txt`), with first-divergence records from
`scripts/pg-plan-first-divergence.py`.

| corpus | match | records vs `routing-20261014h` |
|---|---|---|
| TPC-DS SF0.25 | 81 / 99 | identical |
| TPC-DS SF1 | 66 / 99 | identical |
| TPC-H | 14 / 22 | identical |

No task is filed: **exhausted at `7dfe93df3`**. Item 10 is live.
