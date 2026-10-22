# M0146-0014 — S9 re-check at 8711a1938 (2026-10-22): exhausted

Trigger: `8711a1938` (M0122-0012 slice 44, the sorted aggregate and the incremental sort stream) touched
`internal/executor` after the `a410e5cf8` pass.

| corpus | capture | match | before (a410e5cf8) |
|---|---|---|---|
| TPC-H | `s9h-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9h-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9h-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
