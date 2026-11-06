# M0146-0014 — census at f70664de8 (2026-10-30): exhausted

Trigger: `f0c7b6718` touched `internal/executor`, but only with a new `_test.go` file (the atttypmod sibling test).
Its production change was in `internal/access/transam/xlog`, which is not a planner path. The census was re-captured
anyway so that the reuse rule holds strictly. The goopg plan text is byte-identical to `routing-20261030a` on all
three corpora; only the header lines differ.

| corpus | capture | match | before (8c85bc43d, routing-20261030a) |
|---|---|---|---|
| TPC-H | `s9w16-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w16-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w16-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
