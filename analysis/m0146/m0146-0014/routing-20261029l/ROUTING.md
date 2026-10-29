# M0146-0014 — census at fdde69739 (2026-10-29): exhausted

Trigger: `fdde69739` (M0122-0021 residual 12) changed `internal/executor`.
- Per-worker EXPLAIN lines now print as PG's per-worker blocks.
- A parallel bitmap heap scan is labelled `Parallel Bitmap Heap Scan`.

No benchmark plan has a parallel bitmap heap scan. The captured plan text is byte-identical to `routing-20261029k` on
all three corpora.

| corpus | capture | match | before (28c6de6c8, routing-20261029k) |
|---|---|---|---|
| TPC-H | `s9w12-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w12-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w12-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
