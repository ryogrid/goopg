(idle — nothing in flight)

## Loop summary (2026-07-12, loop #81)

**Task (M0122-0003 sub-slice):** registered the two remaining unregistered
*global* single-row cluster stat views — `pg_stat_bgwriter` and
`pg_stat_archiver`. Previously `SELECT ... FROM` either errored with
unknown-relation. These are cluster-wide summaries (not per-object row sets),
so they follow the `pg_stat_wal`/`pg_stat_slru` precedent, NOT the per-object
`pg_stat_*_tables` executor-twin pattern.

- `pg_stat_bgwriter` (OID 3406): PG 17+ 4-col shape `buffers_clean/
  maxwritten_clean/buffers_alloc/stats_reset` (checkpoint cols already in the
  earlier `pg_stat_checkpointer`). All counters honest 0 (no bgwriter counter
  accumulator wired).
- `pg_stat_archiver` (OID 3407): 7-col shape; counts 0, all four `last_*`
  cells NULL (`catalog.VirtualNull`) — matches `archive_mode=off`.
- Both registered as static single-row virtual views in
  `registerSystemTables` (internal/catalog/catalog.go, right after
  pg_stat_wal). NO executor twin, NO relcache_init entry (matches pg_stat_wal).
- Tests: internal/catalog/pgstat_global_test.go (2) +
  internal/executor/pgstat_global_e2e_test.go (2) — PASS.
- Design: docs/design/0122-0003-pg-stat-user-tables.md new "Global cluster
  views" section + README row. Ledger row + fix_plan note added.

**Gates:** catalog+executor full packages PASS; go build ./... clean;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook.
Committed + pushed.

**Nightly:** action-items.md run 20260712-020530 unchanged — same 39-item
co-load cascade already triaged loops #79/#80. No new M-NIGHTLY.

**Still-unregistered pg_stat views (next natural slices):** `pg_stat_database`
(per-DB, ~30 cols, honest-0 like the others), `pg_stat_database_conflicts`,
`pg_stat_ssl`, `pg_stat_gssapi`, `pg_stat_progress_*` (vacuum/analyze/
create_index/cluster/basebackup/copy), `pg_stat_subscription_stats`. Or pick a
different milestone slice — the per-object + global stat-view family is now
substantially complete.

In-flight: none
