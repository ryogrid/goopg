# M0146-0014 — census at ee57789ff (2026-10-29): exhausted

Trigger: `ee57789ff` (M0122-0021) changed `internal/executor`: EXPLAIN ANALYZE now defaults BUFFERS on, as PG 18
does. The census captures plain EXPLAIN, which this change does not touch.

| corpus | capture | match | before (fdde69739, routing-20261029l) |
|---|---|---|---|
| TPC-H | `s9w13-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w13-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w13-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.

Side note, recorded in the deferral ledger: `make ea-ratchet` fails with the same 8 NEW findings on `ee57789ff`, on
the HEAD binary and on the session-start binary `997db829b`. The failure predates this session's commits.
