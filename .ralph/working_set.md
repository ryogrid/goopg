(idle — nothing in flight)

## Loop summary (2026-07-12, loop #84)

**M0122-0003 — registered `pg_stat_database_conflicts`** (per-database
recovery-conflict view, 8-col PG 18.3 shape, OID 9102). M-NIGHTLY was already
clean: run 20260712-020530's 39-item testport cascade was triaged in loop #82 as
a stale build break; re-verified this loop (go build ./... clean, go vet
testport clean, TestPort_IsolationStats + TestPort_PgAmcheck002Nonesuch PASS).
No newer nightly. Proceeded to the active pg_stat-view slice.

- Like `pg_stat_database` this view is GLOBAL (one row per database via
  `catalog.PGStatDatabaseConflictsRows` → `c.ListDatabases()` at query time,
  CREATE/DROP DATABASE reflected immediately, NO per-connection executor twin).
  UNLIKE it: NO leading shared `datid=0` row — upstream `system_views.sql` is a
  bare `FROM pg_database D` (confirmed from PG source).
- All 6 `confl_*` counters faithful 0 — they only bump on a STANDBY via
  `pgstat_report_recovery_conflict`; goopg is a primary with no recovery-conflict
  accumulator. VERIFIED byte-identical to a throwaway real PG 18.3 cluster
  (8 cols oid/name/6×bigint, one row per db, no datid=0 row, all confl_* 0).
- `datid` reuses the shared `catalog.databaseDisplayOID(name)` helper so it joins
  pg_database.oid byte-for-byte, same as pg_stat_database.

Files: internal/catalog/catalog.go (PGStatDatabaseConflictsRows + registration
after pg_stat_database), internal/catalog/pgstat_global_test.go,
internal/executor/pgstat_global_e2e_test.go, design 0122-0003 + README + ledger
+ fix_plan.

Gates run: catalog pkg (-run PGStat) PASS; executor (-run PgStat|PGStat) PASS;
go build ./... clean; ralph-state-guard PASS; pgbench smoke via pre-commit hook.

Next natural slice (still-unregistered pg_stat views): pg_stat_ssl,
pg_stat_gssapi, pg_stat_subscription_stats, pg_stat_progress_*,
pg_stat_replication, pg_stat_wal_receiver.
In-flight: none
