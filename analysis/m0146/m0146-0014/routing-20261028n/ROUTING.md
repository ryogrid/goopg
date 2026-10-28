# M0146-0014 — S9 re-check at d0f1515c0 (2026-10-28): exhausted

Trigger: `d0f1515c0` (M0122-0012 C53, slice 123) changed `internal/executor`: the compiled binary arm calls the
NUMERIC arithmetic function itself. Plan-neutral by construction.

| corpus | capture | match | before (de5bd21cc) |
|---|---|---|---|
| TPC-H | `s9bi-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9bi-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9bi-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
