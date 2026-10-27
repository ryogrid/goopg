# M0146-0014 — S9 re-check at f5efcefdf (2026-10-26): exhausted

Trigger: `f5efcefdf` changed `internal/optimizer` after the `51326adf2` pass. That commit is M0147-0016: the dead
`GOOPG_RELSIZE_FALLBACK` env read and the uncalled `envFloatDefault` are retired.

| corpus | capture | match | before (51326adf2) |
|---|---|---|---|
| TPC-H | `s9d-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9d-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9d-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
