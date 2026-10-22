# M0146-0014 — S9 re-check at 49da13013 (2026-10-22): exhausted

Trigger: `49da13013` (M0122-0012 slice 57, the skip walk reads its group position off one comparison) touched
`internal/executor` after the `de41f8c20` pass.

| corpus | capture | match | before (de41f8c20) |
|---|---|---|---|
| TPC-H | `s9s-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9s-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9s-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
