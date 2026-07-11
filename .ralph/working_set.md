(idle — nothing in flight)

## Loop summary (2026-07-12, loop #80)

**Task (M0122-0003 sub-slice):** added the `pg_stat_xact_all_tables` /
`pg_stat_xact_user_tables` / `pg_stat_xact_sys_tables` trio — the
per-transaction table access-stat views, the last unregistered
`pg_stat_*_tables` family. Transaction-scoped counterpart of the cumulative
`pg_stat_*_tables` views (loops #75–#78).

- `catalog.PGStatXactTablesRowsForDBOid(dbOid, scope)` reuses the IDENTICAL
  relation filter + `StatTableScope` user/sys split as
  `PGStatTablesRowsForDBOid`; narrower 12-col shape (relid/schemaname/relname +
  9 `pg_stat_get_xact_*` delta counters, NO n_live_tup/last_*/vacuum cells).
  goopg has no `PgStat_TableXactStatus` accumulator ⇒ every delta counter = 0
  (faithful for an untouched relation). Identity cells real.
- `executor.fetchStatXactTablesRows` per-connection twin (internal/executor/
  pgstat_tables.go); wired at `valuesOp.Open`'s three new
  `pg_stat_xact_*_tables` branches (operators.go). Static VirtualRows OIDs
  9098–9100 registered in `registerSystemTables`.
- Tests: internal/catalog/pgstat_xact_tables_test.go (4) +
  internal/executor/pgstat_xact_tables_e2e_test.go (2) — PASS.
- Design: docs/design/0122-0003-pg-stat-user-tables.md new "Transaction
  sibling" section + README row. Ledger row + fix_plan note added.

**Gates:** catalog+executor full packages PASS; go build ./... clean;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook.
Committed + pushed.

**Nightly:** action-items.md run 20260712-020530 all 39 items = one co-load
cascade (121 connection-refused to dead isolation server :39219 + package
2h12m timeout `signal: killed`). Already triaged loop #79. No new M-NIGHTLY.

**Next natural slice:** the `pg_stat*` per-object view family is now COMPLETE
(tables/indexes/sequences/functions cumulative + xact-tables). Remaining
M0122-0003 gaps are the larger cross-cutting counter subsystems (per-table/
per-index/per-relation-buffer/per-function/per-xact accumulators — see ledger),
or pick a different milestone slice.

In-flight: none
