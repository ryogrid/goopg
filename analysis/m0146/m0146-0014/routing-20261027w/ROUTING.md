# M0146-0014 — S9 re-check at 986df5900 (2026-10-27): exhausted

Trigger: `986df5900` (M0122-0012 slice 103) changed `internal/executor` after the `64aa8f62f` pass (the intervening
`67c83ea50` touched storage only): the bitmap heap scan joins the scan borrow.

| corpus | capture | match | before (64aa8f62f) |
|---|---|---|---|
| TPC-H | `s9an-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9an-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9an-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
