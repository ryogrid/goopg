# M0146-0014 — S9 re-check at 2e8519eff (2026-10-24): exhausted

Trigger: `2e8519eff` changed `internal/executor` after the `8532a71e3` pass. That commit is M0122-0012 slice 92:
cooperative hash build producers borrow their scan and fill recycled batch blocks.

| corpus | capture | match | before (8532a71e3) |
|---|---|---|---|
| TPC-H | `s9e-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9e-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9e-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
