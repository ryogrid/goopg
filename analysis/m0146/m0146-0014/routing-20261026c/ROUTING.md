# M0146-0014 — S9 re-check at 9c3e12d47 (2026-10-26): exhausted

Trigger: `9c3e12d47` changed `internal/optimizer` after the `f5efcefdf` pass. That commit is M0147-0017: the
`GOOPG_DISABLE_NLI` env switch and the process-wide NLI seed are retired.

| corpus | capture | match | before (f5efcefdf) |
|---|---|---|---|
| TPC-H | `s9e-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9e-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9e-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
