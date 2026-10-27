# M0146-0014 — S9 re-check at 1a41444e5 (2026-10-25): exhausted

Trigger: `483b19112` changed `internal/optimizer` (and its tests) after the `50ecb096b` pass. That commit is
M0147-0013: the correlated scalar-sublink decorrelation (`GOOPG_SCALAR_UNNEST`) is retired. `1a41444e5` is
analysis-only, so the capture image is code-identical to `483b19112`.

| corpus | capture | match | before (50ecb096b) |
|---|---|---|---|
| TPC-H | `s9a-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9a-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9a-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
