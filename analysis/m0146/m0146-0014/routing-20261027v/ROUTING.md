# M0146-0014 — S9 re-check at 64aa8f62f (2026-10-27): exhausted

Trigger: `64aa8f62f` (M0122-0012 slice 101) changed `internal/executor` after the `06d18e112` pass (the
intervening `2a73e7850` and `32e9a7306` touched no planner-relevant path): a `%`-only LIKE pattern is matched as
literal segments.

| corpus | capture | match | before (06d18e112) |
|---|---|---|---|
| TPC-H | `s9am-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9am-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9am-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
