(idle — nothing in flight)

## Loop summary (2026-07-12, loop #77)

**Task (M0122-0003 sub-slice):** added the six per-relation buffer-pool I/O
statistics system views `pg_statio_all/user/sys_tables` (11 cols) +
`pg_statio_all/user/sys_indexes` (7 cols) — the I/O sibling of the per-table/
per-index access-stat views landed the same day (loops #75/#76).

- `catalog.PGStatioTablesRowsForDBOid(dbOid, scope)` +
  `PGStatioIndexesRowsForDBOid(dbOid, scope)` (internal/catalog/catalog.go):
  reuse the SAME relation filter + `StatTableScope` user/sys split as the
  access-stat twins; index builder enumerates `AllIndexes(dbOid)`. Every
  heap/idx/toast/tidx block counter is a faithful 0 (goopg has no per-relation
  buffer-pool attribution — pg_stat_io counters are pool-wide); identity cells
  real.
- Registered 6 virtual views (OIDs 9087–9092) in `registerSystemTables`.
- `executor.fetchStatioTablesRows`/`fetchStatioIndexesRows`
  (internal/executor/pgstat_tables.go) are the per-connection twins; wired at
  valuesOp.Open (internal/executor/operators.go) via six new branches.
- Tests: internal/catalog/pgstatio_test.go +
  internal/executor/pgstatio_e2e_test.go — PASS.
- Design: docs/design/0122-0003-pg-stat-user-tables.md new "I/O sibling"
  section + README row. Ledger row + fix_plan banner note appended.

**Gates:** catalog+executor full packages PASS; go build ./... clean;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook.
Committed + pushed.

**Nightly:** action-items.md run 20260712-020530 = 39 testport items, all the
same 121-connection-refused co-load cascade already triaged by loops #75/#76.
Confirmed non-regression: local `TestPort_IsolationStats` PASS in 3.5s. No new
M-NIGHTLY task.

**Next natural slice:** `pg_statio_all/user/sys_sequences` trio (relkind 'S',
5 cols `relid/schemaname/relname/blks_read/blks_hit`) — needs a distinct
sequence-enumeration filter (`t.IsSequence`); OIDs 9093–9095. See ledger row.

In-flight: none
