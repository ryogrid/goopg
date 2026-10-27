# M0146-0014 — S9 re-check at 4f7b216cd (2026-10-26): exhausted

Trigger: `4f7b216cd` changed `internal/optimizer` after the `aafabf804` pass. That commit is M0147-0022: the PG
`cost_memoize_rescan` entry currency (`GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST`) is promoted.

| corpus | capture | match | before (aafabf804) |
|---|---|---|---|
| TPC-H | `s9h-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9h-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9h-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
