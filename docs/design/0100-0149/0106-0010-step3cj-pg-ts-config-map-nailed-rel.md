# M0106-0010 Step 3cj — pg_ts_config_map nailed local rel + index 3609

Status: accepted (2026-05-18)

## Goal

Close the FATAL `could not open relation with OID 3603` PG-standby boot
blocker that surfaces immediately after Step 3ci seeded the pg_transform
family (heap 3576 + indexes 3574/3575). OID 3603 is `pg_ts_config_map` per
`postgres/src/include/catalog/pg_ts_config_map.h:30`
(`CATALOG(pg_ts_config_map,3603,TSConfigMapRelationId)`).

## Why now

The Step 3ci E2E re-run shifted the first standby-side FATAL from
`could not open relation with OID 3576` to `could not open relation with
OID 3603`. The pg_ts_config_map catalog is opened during PG-standby boot
once the pg_transform family is cleared (PG walks every declared SYSCACHE
on startup, and `TSCONFIGMAP` is declared at
`postgres/src/include/catalog/pg_ts_config_map.h:50`).

## What ships

Family-complete seed: heap 3603 + the single declared index 3609
(`pg_ts_config_map_index`, UNIQUE PRIMARY btree(mapcfg oid_ops,
maptokentype int4_ops, mapseqno int4_ops), backs
`MAKE_SYSCACHE(TSCONFIGMAP, …, 2)`).

### (a) Schema — `pgTsConfigMapAttrs()` in `relcache_init.go`

Sourced verbatim from `pg_ts_config_map.h:30-43` + `pg_ts_config_map_d.h`
(Anum_pg_ts_config_map_* 1..4, Natts_pg_ts_config_map == 4):

| attnum | name         | TypeOID    | Len | NotNull | notes                       |
|--------|--------------|------------|-----|---------|-----------------------------|
| 1      | mapcfg       | 26 (oid)   | 4   | true    | BKI_LOOKUP(pg_ts_config)    |
| 2      | maptokentype | 23 (int4)  | 4   | true    |                             |
| 3      | mapseqno     | 23 (int4)  | 4   | true    |                             |
| 4      | mapdict      | 26 (oid)   | 4   | true    | BKI_LOOKUP(pg_ts_dict)      |

pg_ts_config_map has no `oid` system column — attnums start at 1 = mapcfg.

`RelType=83` (the safe synthesized rowtype OID) is correct because
pg_ts_config_map is not formrdesc'd — only pg_database/pg_authid/
pg_auth_members/pg_shseclabel/pg_subscription are formrdesc'd shared rels
per `postgres/src/backend/utils/cache/relcache.c:4075-4083`, so the
Phase3 `relation->rd_att->tdtypeid == relp->reltype` assertion at
`relcache.c:4293` does not fire on pg_ts_config_map.

### (b) `nailedLocalRels` (relcache_init.go) gains

- heap entry `{3603, "pg_ts_config_map", 83, 'r', 4, false, pgTsConfigMapAttrs()}`
  after the Step 3ci pg_transform entry
- idxSpec entry `{3609, "pg_ts_config_map_index"}` after the Step 3ci 3575 entry

### (c) `pgIndexInitialEntries` local section (initdb.go) gains

- `entry(3609, 3603, []int16{1, 2, 3}, []uint32{oidOps, int4Ops, int4Ops}, []uint32{0, 0, 0}, true, true)`

after the Step 3ci 3575 entry. Heap attnums: 1=mapcfg, 2=maptokentype,
3=mapseqno. No collation (oid_ops + int4_ops are non-collatable).

### (d) Critical index placeholder OID lists

Both blocks in `bootstrapPostgresDatabase` (`base/<dboid>/` + `global/`
fallback) gain `3609` after the Step 3ci 3575 entry.

### (e) `bootstrapMappedLocalCatalogHeaps` + `localRelMap`

Both gain `3603` (authoritative pg_ts_config_map OID per
`pg_ts_config_map.h:30`). The pre-existing stale `3765 // pg_ts_config_map`
placeholder is left in place — 3765 has no upstream catalog assignment so
the empty 8 KiB heap at `base/{1,5}/3765` is harmless — and its comment is
updated to flag the historical mislabel.

### (f) Type helpers — no change required

`oid` (26) and `int4` (23) are already registered in `pgCatalogTypeOID` /
`pgCatalogTypeLen` / `pgTypeByVal` / `pgTypeAlignChar` /
`pgTypeStorageChar`.

## Regression pins

New tests in `internal/initdb/pg_ts_config_map_nailed_test.go`:

- `TestNailedLocalRelsContainsPgTsConfigMap` — pin heap entry + every column
  descriptor
- `TestNailedLocalRelsContainsPgTsConfigMapIndexes` — pin index entry with
  correct RelKind/RelNatts
- `TestPgTsConfigMapIndexInitialEntries` — pin IndKey/IndClass/IndCollation/
  IsUnique/IsPrimary against the PG18 declaration
- `TestPgTsConfigMapAttrsTypeOIDsMatchPG18` — pin exact TypeOID/Len/NotNull

Existing tests extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` map gains `3609:{1,2,3}`
  (strict count guard forces every future addition to update this)
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  gains 3609

## Verification

- `go build ./...` PASS
- targeted `go test -count=1 -run 'TestNailedLocalRelsContainsPgTsConfigMap|
  TestPgTsConfigMapIndexInitialEntries|TestPgTsConfigMapAttrsTypeOIDsMatchPG18|
  TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|
  TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|
  TestNailedIndexRelnattsAgreesWithIndnatts|TestPgClassOidIndexHasSingleKeyColumn|
  TestPgIndexColDefsMatchesRelcacheAttrs' ./internal/initdb/` PASS
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3ci (TestMigrationFromLegacyJSONCluster,
  TestMigrationIdempotent, TestMigrationPGAttributeRowsWritten,
  TestCommittedTableSurvivesCrashRestart, TestRuntimeCloseTriggersFinalCheckpoint,
  TestCreateTableSurvivesRestartViaCatalogHeap, TestMultipleTablesLoadFromHeap,
  TestCreateIndexSurvivesRestartViaWAL, TestCreateIndexRecoveredOIDDoesNotCollide,
  TestSystemCatalogRelfilesAreValidHeapPages, TestBootstrappedPGClassRowsReadable,
  TestBootstrappedPGAttributeRowsReadable, TestOpenOldClusterWithoutM0030FilesStillWorks,
  TestSynchronousCommitFlushesByDefault). No new regressions.
- cross-package smoke `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` PASS
- E2E re-run `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -run
  TestE2E_FailoverGoopgToPG/async ./internal/testport/` confirms FATAL on
  3603 is closed; the next standby-side FATAL is `could not open relation
  with OID 3602` (pg_ts_config per `pg_ts_config.h:30`), which is
  Step 3ck territory.

## Files changed

- `internal/initdb/relcache_init.go` — heap entry, idxSpec entry, and
  new `pgTsConfigMapAttrs()` function
- `internal/initdb/initdb.go` — `pgIndexInitialEntries` local section,
  `bootstrapMappedLocalCatalogHeaps` oids list, `localRelMap`, both
  critical-index placeholder OID lists
- `internal/initdb/pg_ts_config_map_nailed_test.go` — new pin file
- `internal/initdb/pg_index_indkey_test.go` — map gains 3609
- `internal/initdb/btree_index_bootstrap_test.go` — mustHave gains 3609
