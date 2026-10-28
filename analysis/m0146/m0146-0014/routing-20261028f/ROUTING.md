# M0146-0014 — S9 re-check at 821d93323 (2026-10-28): exhausted

Trigger: `821d93323` (M0122-0012 C38, slice 115) changed `internal/executor`: the streaming nested loop's outer seq
scan decodes only the join filter's columns until a pair is used. Plan-neutral by construction.

| corpus | capture | match | before (f645129a8) |
|---|---|---|---|
| TPC-H | `s9ba-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9ba-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9ba-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
