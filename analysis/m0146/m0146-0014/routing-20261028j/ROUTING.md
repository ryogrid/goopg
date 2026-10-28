# M0146-0014 — S9 re-check at 12f21f440 (2026-10-28): exhausted

Trigger: `12f21f440` (M0122-0012 C48, slice 119) changed `internal/executor` and `internal/storage`: the seq scan
fetches its heap tuple in place. Plan-neutral by construction.

| corpus | capture | match | before (102c74a5e) |
|---|---|---|---|
| TPC-H | `s9be-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9be-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9be-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
