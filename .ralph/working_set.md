Task: M0122-0007 — pg_ts_config per-DB routing (landed)

Files:
- internal/catalog/catalog.go: UserTSConfig.DBOid field, CreateTSConfig/FindTSConfig/DropTSConfig dbOid variadic params, ListUserTSConfigs dbOid filtering, PGTSConfigRowsForDBOid method, CreateTSConfigDuringRecovery DBOid fallback, pgTSConfig/pgTSConfigMap VirtualRows update
- internal/executor/operators_ddl.go: 10 call sites updated to pass catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)
- internal/initdb/catalog_heap_reload.go: UserTSConfig literal includes DBOid: cat.DBOID()
- internal/server/dispatch.go: pgTSConfigRowLister interface + wireExtensionRows PgTSConfigRows
- internal/executor/context.go: PgTSConfigRows field
- internal/executor/operators.go: pg_ts_config per-connection override
- internal/catalog/create_tsconfig_dbscope_test.go: cross-DB isolation test (PASS)
- test files updated: tsconfig_replacedict, tsconfig_rename_setschema_dropmapping, tsconfig_copy

Key symbols:
- catalog.UserTSConfig (new DBOid field)
- catalog.PGTSConfigRowsForDBOid (new method, mirrors PGTSDictRowsForDBOid)
- server.pgTSConfigRowLister (new interface)
- executor.Context.PgTSConfigRows (new field)

Hypothesis/Findings:
- UserTSConfig lacked DBOid, leaking configs across databases
- Fixed by following the exact UserTSDict pattern (dbOid variadic param, resolveDBOid convention)
- pg_ts_config_map also uses ListUserTSConfigs; updated its VirtualRows to pass DefaultDBOid explicitly
- The mapcfg-filtered WHERE clause in pg_ts_config_map queries means cross-DB leakage there is cosmetic (config OIDs won't match across databases), so per-connection override deferred

Next step:
Per the Current Priority banner: M-NIGHTLY (clear) → M0119 (blocked on large items) → M0122.
Next M0122 slice: either M0122-0003 EXPLAIN WAL/MEMORY output (deferral ledger rows 2026-08-08)
or a follow-up on the design doc 0122-0018's "Still deferred" items (only "type catalog rows" remain — sequences and constraints are already done).

Gates run:
- go build ./...: PASS
- go vet: PASS
- catalog tests: PASS (0.068s)
- executor tests: PASS (5.894s)
- server tests: PASS (23.412s)
- initdb tests: PASS (54.573s)
- TestCreateTSConfigCrossDatabaseIsolation: PASS
- make ralph-state-guard: OK

In-flight: none
