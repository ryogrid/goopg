(idle — nothing in flight)

## Loop summary (2026-07-12, loop #79)

**Task (M0122-0003 sub-slice):** added the `pg_stat_user_functions` +
`pg_stat_xact_user_functions` views — the LAST per-object stat views in the
`pg_stat*` family (tables/indexes/sequences landed loops #75–#78).

- Both upstream views (`system_views.sql`) filter on
  `pg_stat_get_function_calls(oid) IS NOT NULL`, never true under the default
  `track_functions = none`, so both are empty on a stock PG 18.3 cluster out of
  the box. goopg has no per-function call/time tracking, so
  `catalog.PGStatUserFunctionsRows()` (internal/catalog/catalog.go) returns nil
  unconditionally — always-empty is the faithful default behaviour, not a
  shortcut.
- Registered 2 virtual views (OIDs 9096–9097) with the exact 6-col tupledesc
  `funcid/schemaname/funcname/calls/total_time/self_time` in
  `registerSystemTables`. Always-empty ⇒ NO per-connection twin and NO
  `valuesOp.Open` branch needed (unlike table/index/sequence views).
- Tests: internal/catalog/pgstat_functions_test.go
  (TestPGStatUserFunctionsRowsAlwaysEmpty / ViewsRegistered) +
  internal/executor/pgstat_functions_e2e_test.go
  (TestPgStatUserFunctionsEndToEnd) — PASS.
- Design: docs/design/0122-0003-pg-stat-user-tables.md new "Function sibling"
  section + README row. Ledger row appended. fix_plan banner note added.

**Gates:** catalog+executor full packages PASS; go build ./... clean;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook.
Committed + pushed (pending this loop).

**Nightly:** action-items.md run 20260712-020530 already fully triaged (39
testport items = 121-connection-refused co-load cascade, non-regression). No
new M-NIGHTLY task.

**Next natural slice:** the `pg_stat_xact_all/user/sys_tables` trio (per-txn
delta table stats) is the last unregistered pg_stat per-object family; OR the
deferred per-relation buffer-pool block attribution + per-function/per-table
counter subsystems (larger cross-cutting storage/executor slices — see ledger).

In-flight: none
