# M0146-0014 — S9 re-check at a410e5cf8 (2026-10-22): exhausted

Trigger: `a410e5cf8` (M0122-0012 slice 43, an unknown literal is coerced once, not per row) touched
`internal/executor` after the `62b168220` pass.

| corpus | capture | match | before (62b168220) |
|---|---|---|---|
| TPC-H | `s9g-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9g-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9g-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
