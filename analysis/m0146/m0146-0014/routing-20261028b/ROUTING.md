# M0146-0014 — S9 re-check at 39041efc5 (2026-10-28): exhausted

Trigger: `39041efc5` (M0122-0012 C34) changed `internal/executor`: index-scan heap fetches skip the columns no node
above reads (a per-execution plan walk published on `Context`). Plan-neutral by construction.

| corpus | capture | match | before (e3a2535f0) |
|---|---|---|---|
| TPC-H | `s9aw-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9aw-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9aw-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
