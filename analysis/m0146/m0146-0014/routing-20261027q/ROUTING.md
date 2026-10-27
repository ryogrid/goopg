# M0146-0014 — S9 re-check at c0d1f97ed (2026-10-27): exhausted

Trigger: `c0d1f97ed` (M0122-0012 slice 95) changed `internal/optimizer` after the `b1f699edf` pass: GEQO tours
share the join-search memos.

| corpus | capture | match | before (b1f699edf) |
|---|---|---|---|
| TPC-H | `s9ah-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9ah-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9ah-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
