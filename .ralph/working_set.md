(idle — nothing in flight)

## Loop summary (2026-07-12, loop #83)

**M0122-0003 — registered `pg_stat_database` (headline per-database cluster
stat view).** M-NIGHTLY was already clean (run 20260712-020530 triaged & fixed
in loop #82; no newer nightly), so proceeded to the active pg_stat-view slice.

- `pg_stat_database` is GLOBAL (lists every DB + a shared `datid=0`/`datname=NULL`
  row, independent of the connected DB) → follows the `pg_stat_bgwriter` static-
  `VirtualRows` precedent, NO per-connection executor twin. Closure is
  `catalog.PGStatDatabaseRows` (enumerates `c.ListDatabases()` at query time →
  CREATE/DROP DATABASE reflected immediately). OID 9101, 30-col PG 18.3 shape,
  registered in `registerSystemTables` after `pg_stat_archiver`.
- All counters honest 0, `numbackends` 0, `checksum_last_failure`/`stats_reset`
  NULL — VERIFIED byte-identical to a fresh real PG 18.3 cluster (spun a
  throwaway instance this loop).
- Sibling-paths: extracted `catalog.databaseDisplayOID(name)`; BOTH
  `pg_database.VirtualRows` and `PGStatDatabaseRows` now route through it, so
  `pg_stat_database.datid` joins `pg_database.oid` byte-for-byte (resolves the
  old "keep this switch in sync" comment on `ResolveDatabaseOid`).

Files: internal/catalog/catalog.go (helper + PGStatDatabaseRows + registration +
pg_database refactor), internal/catalog/pgstat_global_test.go,
internal/executor/pgstat_global_e2e_test.go, design 0122-0003 + README + ledger +
fix_plan.

Gates run: catalog pkg PASS; executor pgstat/database tests PASS; go build ./...
clean; ralph-state-guard PASS; pgbench smoke via pre-commit hook (on commit).

Next natural slice (still-unregistered pg_stat views): pg_stat_database_conflicts,
pg_stat_ssl, pg_stat_gssapi, pg_stat_subscription_stats, pg_stat_progress_*,
pg_stat_replication, pg_stat_wal_receiver.
In-flight: none
