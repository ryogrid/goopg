# 02 — goopg current state (audit matrix)

Base: `waitevent-impl` @ 44aa63b0a. Citations `internal/...`.

## 1. Parity matrix

| Area | PG 18.3 (+file:line) | goopg (+file:line) | Verdict |
|---|---|---|---|
| Heap scan VM skip | skips all-visible runs ≥32 (`vacuumlazy.c:209,1568–1656`) | **no skip** — every block pinned & processed; only IsNew passed over (`commands/vacuum/vacuum.go:108–120`); `VM.AllVisible` never consulted by vacuum (sole skip consumer is index-only scans, `executor/operators_indexonly.go:203`) | DIVERGE |
| Aggressive mode | cutoff-driven full scan, disables skipping (`vacuumlazy.c:777,793–799`; `vacuum.c:1244–1273`) | absent; no failsafe/eager concepts at all | DIVERGE |
| FREEZE option | zeroes all four freeze ages ⇒ aggressive (`vacuum.c:404–417`) | parsed (`parser/parser.go:1949–1950,1986–1993`, ast.go VacuumStmt.Freeze) but **never read by executor** — VACUUM (FREEZE) behaves as plain VACUUM | DIVERGE |
| FreezeLimit math | nextXID − min(min_age, max_age/2), clamp ≤ OldestXmin (`vacuum.c:1195–1209`) | `freezeBelow = NextXID − FreezeMinAge` when GUC > 0, no cap/clamp (`executor/operators_vacuum.go:64–70`); launcher hardcodes 50M (`autovacuum/launcher.go:139`) instead of the GUC | PARTIAL |
| relfrozenxid skip-guard | skipped all-vis-not-all-frozen pages block advancement (`vacuumlazy.c:876–893`) | advanced whenever freezing ran (`operators_vacuum.go:171–177`); safe today only because nothing is ever skipped — guard must land with skipping | MATCH (contingent) |
| Multixact freeze bookkeeping | MultiXactCutoff, relminmxid advance, wraparound check (`vacuum.c:1224–1229`, `autovacuum.c:3066–3072`) | no relminmxid field/cutoff/freezing of multis (`storage/freeze.go` resolves multis only for liveness); GUCs inert (`utils/misc/defaults.go:1051`) | DIVERGE |
| Autovacuum vacuum trigger | dead_tuples > 50 + 0.2·reltuples (+insert-only rule) (`autovacuum.c:3109–3157`) | `Stats==nil → true` else `RowCount > 0` gated on wall-clock MinVacuumAge=5m — every non-empty table each tick (`autovacuum/launcher.go:231–236,132`) | DIVERGE |
| Autovacuum analyze trigger | mod_since_analyze > 50 + 0.1·reltuples | unconditional (modulo enabled flag), every ≥5 min (`launcher.go:240–245`) | DIVERGE |
| Anti-wraparound | forces past enabled=off, drops SKIP_LOCKED, DB priority (`autovacuum.c:1132,2844,3082–3087`) | override present before enabled-check (`launcher.go:220–232`); no ordering / SKIP_LOCKED distinction | PARTIAL |
| Launcher wiring | naptime-scheduled per-DB workers, max_workers=3 | **never started in production**: `ServerConfig.AutovacuumLauncher` exists (`postmaster/server.go:220`, started :633–642 if set) but only tests call NewLauncher; defaults hardcoded 60s/5m/5m/1 (`launcher.go:69–72`), WorkerLimit logged not enforced | DIVERGE |
| Cost throttling | balance model + sleeps (`vacuum.c:2472–2490`) | none; `vacuum_cost_delay` registered-inert (`defaults.go:920–925`) | DIVERGE |
| ANALYZE sampling | 300×target rows via random blocks + Vitter (`analyze.c:493–521,1166–1185`) | executor path reservoir-samples with `sampleCap = target×300` and builds NDistinct/NullFrac/MCV/histogram/correlation, persists pg_statistic + sidecar (`executor/operators_analyze.go:395–955,297–368`) — full-scan reservoir rather than block-stage (acceptable superset) | MATCH |
| autoanalyze quality | same sampled analyzer | launcher calls simplified `commands/vacuum.Analyze`: full scan, Rows/AvgWidth only, column stats discarded (`commands/vacuum/vacuum.go:225–268`; caller `autovacuum/launcher.go:166–176`) | DIVERGE |
| Inherited stats | acquire_inherited_sample_rows | partitioned parents get lock-wait emulation only (`operators_analyze.go:134–141`) | DEFERRED |
| VACUUM FULL / CLUSTER | physical rewrite/reorder | Full → AccessExclusiveLock only (`operators_vacuum.go:83–86`); CLUSTER records indisclustered, "no physical reorder" (`executor/operators_cluster.go:4–6`) | DEFERRED |
| Truncation | conditional tail truncate honoring GUC/reloption (`vacuumlazy.c:862,3197–3230`) | never truncates; `vacuum_truncate` registered-inert (`defaults.go:791–809`) | DIVERGE |
| relallvisible publish | vac_update_relstats writes it (`vacuum.c:1442`) | not published | PARTIAL |
| VM lifecycle on DML | cleared by heapam on insert/update/delete | cleared on insert/delete/update sites (`executor/operators_storage.go:9099,9245,9292,9403,9539,9655–9656,9761`) + DropRelation; WAL redo ports bit math (`storage/vm_redo.go`) | MATCH |
| datfrozenxid refresh | unconditional after DB-wide VACUUM (`vacuum.c:723`) | `_ = persistDatFrozenXID(ctx)` + GRANT/REVOKE wait emulation (`operators_vacuum.go:215,226–240`) | MATCH |

## 2. Dead-tuple accounting (trigger input) — missing

- `catalog.TableStats` = {RowCount, Pages, AvgWidth, Columns, Analyzed} only
  (`catalog/catalog.go:1649–1674`). No dead/mod/insert counters.
- Executor pgstat layer has a partial analog: `relStatCounters.deltaDead`
  accumulates tuplesUpdated+tuplesDeleted at commit fold
  (`executor/pgstat_relations.go:44–58,249–258,349–352`), exposed as
  `pg_stat_get_dead_tuples(oid)` (`executor/expr.go:17368–17410`), never
  reset by VACUUM ("No VACUUM-driven relation stats yet", expr.go:17412).
- `pg_stat_user_tables` emits hardcoded zeros for n_dead_tup /
  n_mod_since_analyze / n_ins_since_vacuum
  (`catalog/catalog.go:7808–7826`).

## 3. Missing-GUC checklist (`internal/utils/misc/defaults.go`)

Registered-but-inert: `vacuum_freeze_min_age`(consumed by manual path,
dispatch.go:1380), `autovacuum_freeze_max_age`(:1042, engine uses const),
`vacuum_multixact_freeze_min_age`(:1051), `vacuum_truncate`(:806),
`vacuum_cost_delay`(:924), `maintenance_work_mem`(:800),
`autovacuum_worker_slots`(:202).

MISSING entirely (SET would fail): `autovacuum`, `autovacuum_naptime`,
`autovacuum_vacuum_threshold`, `autovacuum_vacuum_scale_factor`,
`autovacuum_vacuum_insert_threshold`,
`autovacuum_vacuum_insert_scale_factor`, `autovacuum_analyze_threshold`,
`autovacuum_analyze_scale_factor`, `autovacuum_vacuum_max_threshold`,
`autovacuum_multixact_freeze_max_age`,
`autovacuum_max_workers`, `autovacuum_vacuum_cost_delay`,
`autovacuum_vacuum_cost_limit`, `vacuum_cost_limit`,
`vacuum_cost_page_hit/miss/dirty`, `vacuum_freeze_table_age`,
`vacuum_multixact_freeze_table_age`, `vacuum_multixact_freeze_max_age`,
`log_autovacuum_min_duration`, `autovacuum_work_mem`,
`vacuum_failsafe_age`, `vacuum_multixact_failsafe_age`.
(Upstream anchors guc_tables.c:1541, 2643–2683, 2794–2843, 3529–3607, 4071–4113.)

## 4. Per-table reloptions

Parse/bounds/round-trip complete in catalog for enabled, vacuum/analyze
threshold+scale, insert pair, cost_delay, log duration, freeze min/max/table
ages, multixact family (`catalog/catalog.go:668–905`,
extraction `executor/operators_ddl.go:2880+`). Runtime consumption: ONLY
`autovacuum_enabled`. Wiring them into the trigger formula is part of C2.
