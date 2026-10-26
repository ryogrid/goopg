# M0146-0014 — S9 re-check at a16b71cdf (2026-10-26): exhausted

Trigger: `a16b71cdf` changed `internal/executor` after the `07d3ad3e9` pass. That commit is M0147-0034: the
in-scan early qual switch (`GOOPG_SCAN_PREFILTER`) is retired, with a test-only force-late seam kept.

| corpus | capture | match | before (07d3ad3e9) |
|---|---|---|---|
| TPC-H | `s9l-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9l-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9l-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
