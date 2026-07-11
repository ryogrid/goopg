(idle — nothing in flight)

## Loop summary (2026-07-12, loop #76)

**Task (M0122-0003 sub-slice):** added the three per-index access-statistics
system views `pg_stat_all_indexes` / `pg_stat_user_indexes` /
`pg_stat_sys_indexes` — the direct sibling of the per-table views landed
earlier the same day.

- `catalog.PGStatIndexesRowsForDBOid(dbOid, scope)` (internal/catalog/catalog.go):
  enumerates `AllIndexes(dbOid)`, filters each index's parent table through the
  SAME relation predicate as `PGStatTablesRowsForDBOid`, emits the 9-col upstream
  row (`relid/indexrelid/schemaname/relname/indexrelname/idx_scan/last_idx_scan/
  idx_tup_read/idx_tup_fetch`) sorted by (schemaname,relname,indexrelname).
  Reuses `StatTableScope` for the identical user/sys split.
- Registered 3 virtual views (OIDs 9084/9085/9086) in `registerSystemTables`.
- `executor.fetchStatIndexesRows` (internal/executor/pgstat_tables.go) is the
  per-connection twin of `fetchStatTablesRows`; wired at valuesOp.Open
  (internal/executor/operators.go) via three new branches.
- Honest-0/NULL: 5 identity cells real, 3 scan counters 0, last_idx_scan NULL.
- Tests: internal/catalog/pgstat_indexes_test.go +
  internal/executor/pgstat_indexes_e2e_test.go — PASS.
- Design: docs/design/0122-0003-pg-stat-user-tables.md new sibling section +
  README row. Ledger row + fix_plan banner note appended.

**Gates:** catalog+executor full packages PASS; go build ./... clean;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook.
Committed + pushed.

**Nightly:** batch 20260712-020530 already triaged by loop #75 (121
connection-refused lines = co-load cascade, not real regressions). No new
M-NIGHTLY tasks.

In-flight: none
