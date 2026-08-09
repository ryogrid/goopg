Task: M0119-0004 — Fix TestPort_TSConfigSurvivesRestart (TSConfig lost on restart)

Files:
- internal/initdb/catalog_heap_reload.go: reloadUserTSConfigsFromHeap — DBOid: cat.DBOID() → catalog.DefaultDBOid

Key symbols:
- reloadUserTSConfigsFromHeap: reload path that reads TS configs from catalog heap
- pgTSConfigRel / upsertTSConfigCatalogRow: write path (hardcodes DefaultDBOid)
- ListUserTSConfigs: filters by uc.DBOid == dbOid (the query-time filter)
- NamespaceDBOid / DefaultDBOid / PostgresDBOid: the dual-mirror catalog DBOid scheme

Hypothesis/Findings:
- Root cause: `reloadUserTSConfigsFromHeap` stamped `DBOid: cat.DBOID()` (= PostgresDBOid=5), but `ListUserTSConfigs` filters by `NamespaceDBOid(CurrentDatabaseOid)` (= DefaultDBOid=1). The write side (`pgTSConfigRel()`) uses DefaultDBOid and mirrors to PostgresDBOid, so the reload correctly reads from the mirrored location but stamps the wrong DBOid.
- TS dicts don't have this bug because `ListUserTSDicts` has no DBOid filter.
- Fix: changed `DBOid: cat.DBOID()` → `DBOid: catalog.DefaultDBOid` in reloadUserTSConfigsFromHeap.
- Deferred: per-DB catalog heap routing for pg_ts_config (the write path always uses DefaultDBOid regardless of creating database). Tracked in deferral ledger.
- Pre-existing failures: TestPort_IsolationMergeUpdate (line count mismatch, normalization issue, unrelated).

Next step: Advance M0119-0004 — run TestPort_PgDumpConnectionSetup to identify the next catalog-view parity gap, or pick up the next M0119 sub-item.

Gates run:
- go build ./...: PASS
- go test ./internal/initdb/... ./internal/catalog/... ./internal/executor/... ./internal/server/...: PASS
- TestPort_TSConfigSurvivesRestart: PASS (was FAILING before fix)
- TestPort_TSDictSurvivesRestart: PASS (no regression)
- RALPH_PRECOMMIT_SCOPE=units + smoke: PASS (0 failed, all 3 workloads)
- make ralph-state-guard: PASS

In-flight: none
