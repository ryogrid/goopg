# M0146-0014 — S9 re-check at de5bd21cc (2026-10-28): exhausted

Trigger: `de5bd21cc` (M0122-0012 C51, slice 122) changed `internal/executor`: the seq scan walks a row for TOAST
pointers only when its tuple carries HEAP_HASEXTERNAL. Plan-neutral by construction.

| corpus | capture | match | before (d6476dbc4) |
|---|---|---|---|
| TPC-H | `s9bh-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9bh-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9bh-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
