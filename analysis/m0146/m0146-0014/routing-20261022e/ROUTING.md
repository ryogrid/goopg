# M0146-0014 — S9 re-check at 8bbcba80e (2026-10-22): exhausted

Trigger: `8bbcba80e` (M0122-0012 slice 40, the legacy Filter evaluates a compiled predicate) touched
`internal/executor` after the `1becea6e4` pass.

| corpus | capture | match | before (1becea6e4) |
|---|---|---|---|
| TPC-H | `s9e-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9e-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9e-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
