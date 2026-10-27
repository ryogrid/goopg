# M0146-0014 — S9 re-check at 602669237 (2026-10-28): exhausted

Trigger: `602669237` (M0122-0012 C43, slice 116) changed `internal/executor`: index, index-only and bitmap scans
evaluate their per-row filter compiled. Plan-neutral by construction.

| corpus | capture | match | before (821d93323) |
|---|---|---|---|
| TPC-H | `s9bb-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9bb-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9bb-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
