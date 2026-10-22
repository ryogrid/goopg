# M0146-0014 — S9 re-check at 18b8ea331 (2026-10-22): exhausted

Trigger: `18b8ea331` (M0122-0012 slice 48, a Sort is a borrow root) touched
`internal/executor` after the `b57f3f698` pass.

| corpus | capture | match | before (b57f3f698) |
|---|---|---|---|
| TPC-H | `s9l-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9l-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9l-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
