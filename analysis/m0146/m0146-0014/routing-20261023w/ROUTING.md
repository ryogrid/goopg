# M0146-0014 — S9 re-check at e9ba4cf5f (2026-10-23): exhausted

Trigger: `e9ba4cf5f` (M0122-0012 slice 84: the compiled evaluator applies AND / OR itself and compares a numeric
with an int in place) touched `internal/executor` after the `a05a55628` pass.

| corpus | capture | match | before (a05a55628) |
|---|---|---|---|
| TPC-H | `s9w-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
