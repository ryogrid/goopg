# M0146-0014 — S9 re-check at b7da10d8d (2026-10-27): exhausted

Trigger: `b7da10d8d` (M0147-0051) changed `internal/optimizer`, `internal/executor` (tests) and `internal/postmaster`
after the `b7d73466c` pass: the legacy NLI post-pass, `memoize.go`, `GOOPG_NLI_COSTGATE` and the
`goopg.enable_nestloop_index` GUC are deleted.

| corpus | capture | match | before (b7d73466c) |
|---|---|---|---|
| TPC-H | `s9ab-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9ab-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9ab-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
