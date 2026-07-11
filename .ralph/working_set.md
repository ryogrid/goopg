(idle — nothing in flight)

## Loop summary (2026-07-12, loop #85)

**M0122-0003 — registered the six `pg_stat_progress_*` command-progress views**
(`pg_stat_progress_vacuum`/`analyze`/`cluster`/`create_index`/`basebackup`/`copy`,
OIDs 9103–9108) as static ZERO-row virtual views. M-NIGHTLY was already clean
(run 20260712-020530's testport cascade triaged loops #82/#84 as a stale build
break; no newer nightly this loop — same run in ci/logs/).

- Upstream each view projects `pg_stat_get_progress_info('<CMD>')`, returning one
  row per backend with an active `pgstat_progress_start_command` slot. goopg has
  NO command-progress instrumentation, so all six are empty — byte-identical to an
  IDLE real PG 18.3 cluster (no VACUUM/ANALYZE/etc. in flight).
- Registered via a local `mkProgressView(name, oid, cols)` helper in
  `registerSystemTables` (internal/catalog/catalog.go) right after
  `pg_stat_database_conflicts`. Columns/types transcribed verbatim from
  `system_views.sql` (CASE-mapped phase/command/type = text, paramN = int8, pid =
  int4, delay_time = float8, *id = oid). No per-connection twin.
- IMPORTANT discovery: `pg_stat_replication`/`pg_stat_wal_receiver`/
  `pg_stat_subscription` ALREADY exist in internal/initdb/replication_views.go
  (live walsender/receiver/subscription rows) — do NOT re-register them.

Files: internal/catalog/catalog.go (mkProgressView + 6 views),
internal/catalog/pgstat_global_test.go (TestPGStatProgressViewsRegistered),
internal/executor/pgstat_global_e2e_test.go (TestPgStatProgressViewsEndToEnd),
design 0122-0003 + README + ledger + fix_plan.

Gates run: catalog+executor pg_stat tests PASS; go build ./... clean; go vet
catalog+executor clean; ralph-state-guard OK (auto-repaired); pgbench smoke via
pre-commit hook.

Next still-unregistered pg_stat views (harder — need session/subscription
registry, belong in initdb NOT base catalog.go): pg_stat_ssl, pg_stat_gssapi
(per-backend, join pg_stat_activity), pg_stat_subscription_stats (per-subscription).
In-flight: none
