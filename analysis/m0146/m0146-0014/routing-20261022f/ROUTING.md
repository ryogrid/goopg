# M0146-0014 — S9 re-check at 62b168220 (2026-10-22): exhausted

Trigger: `62b168220` (M0122-0012 slice 42, one identity-key memo per search and an indexed
`sameClass`) touched `internal/optimizer` after the `8bbcba80e` pass (reused at `a0a569de7`).

| corpus | capture | match | before (8bbcba80e) |
|---|---|---|---|
| TPC-H | `s9f-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9f-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9f-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
