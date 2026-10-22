# M0146-0014 — S9 re-check at 984b3cb89 (2026-10-22): exhausted

Trigger: `984b3cb89` (M0122-0012 slice 45, the nested-loop join filter is compiled) touched
`internal/executor` after the `8711a1938` pass.

| corpus | capture | match | before (8711a1938) |
|---|---|---|---|
| TPC-H | `s9i-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9i-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9i-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
