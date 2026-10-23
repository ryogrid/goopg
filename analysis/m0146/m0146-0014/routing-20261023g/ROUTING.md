# M0146-0014 — S9 re-check at 97086429c (2026-10-23): exhausted

Trigger: `97086429c` (M0122-0012 slice 68, numeric division takes a 128-bit path and drops redundant big.Int
copies, and hash-aggregate groups come from chunks) touched `internal/executor` after the `b3f708aee` pass.

| corpus | capture | match | before (b3f708aee) |
|---|---|---|---|
| TPC-H | `s9g-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9g-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9g-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
