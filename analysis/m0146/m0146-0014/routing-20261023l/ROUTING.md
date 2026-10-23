# M0146-0014 — S9 re-check at d92a36e6f (2026-10-23): exhausted

Trigger: `d92a36e6f` (M0122-0012 slice 72, a participant probing a leader-prebuilt shared hash table takes the
empty-inner exit) touched `internal/executor` after the `2efa8fe12` pass.

| corpus | capture | match | before (2efa8fe12) |
|---|---|---|---|
| TPC-H | `s9l-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9l-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9l-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
