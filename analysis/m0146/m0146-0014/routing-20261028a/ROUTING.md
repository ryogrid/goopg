# M0146-0014 — S9 re-check at e3a2535f0 (2026-10-28): exhausted

Trigger: `e3a2535f0` (M0122-0012 C32 slice 1) changed `internal/executor` after the `3d50052e6` pass (`2349ef9b0`
is docs-only): a hash probe's seq scan decodes past its keys only for the rows the join uses.

| corpus | capture | match | before (3d50052e6) |
|---|---|---|---|
| TPC-H | `s9av-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9av-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9av-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
