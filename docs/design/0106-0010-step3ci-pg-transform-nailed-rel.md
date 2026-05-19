# M0106-0010 Step 3ci — pg_transform nailed local rel + indexes 3574/3575

Status: accepted (2026-05-18)

## Goal

Close the FATAL `could not open relation with OID 3576` PG-standby boot
blocker that surfaces immediately after Step 3ch seeded the pg_tablespace
family (heap 1213 + indexes 2697/2698). OID 3576 is `pg_transform` per
`postgres/src/include/catalog/pg_transform.h:29`
(`CATALOG(pg_transform,3576,TransformRelationId)`).

## Why now

The Step 3ch E2E re-run shifted the first standby-side FATAL from
`could not open relation with OID 1213` to `could not open relation with
OID 3576`. The pg_transform catalog is opened during PG-standby boot once
the pg_tablespace family is cleared (PG walks every declared SYSCACHE on
startup, and `TRFOID` / `TRFTYPELANG` are declared at
`postgres/src/include/catalog/pg_transform.h:46-47`).

## What ships

Family-complete seed: heap 3576 + both declared indexes 3574/3575
(`pg_transform_oid_index`, UNIQUE PRIMARY btree(oid oid_ops), backs
`MAKE_SYSCACHE(TRFOID, …, 16)`) and 3575 (`pg_transform_type_lang_index`,
UNIQUE btree(trftype oid_ops, trflang oid_ops), backs
`MAKE_SYSCACHE(TRFTYPELANG, …, 16)`).

### (a) Schema — `pgTransformAttrs()` in `relcache_init.go`

Sourced verbatim from `pg_transform.h:29-36` + `pg_transform_d.h`
(Anum_pg_transform_* 1..5, Natts_pg_transform == 5):

| attnum | name       | TypeOID         | Len | NotNull | notes                          |
|--------|------------|-----------------|-----|---------|--------------------------------|
| 1      | oid        | 26 (oid)        | 4   | true    | system column                  |
| 2      | trftype    | 26 (oid)        | 4   | true    | BKI_LOOKUP(pg_type)            |
| 3      | trflang    | 26 (oid)        | 4   | true    | BKI_LOOKUP(pg_language)        |
| 4      | trffromsql | 24 (regproc)    | 4   | true    | BKI_LOOKUP_OPT(pg_proc) — 0 OK |
| 5      | trftosql   | 24 (regproc)    | 4   | true    | BKI_LOOKUP_OPT(pg_proc) — 0 OK |

BKI_LOOKUP_OPT means the FK lookup is optional (the column stores 0 when
the transform has no fromsql / tosql function), but the column itself is
still NOT NULL — no BKI_FORCE_NULL annotation in the source.

`RelType=83` (the safe synthesized rowtype OID) is correct because
pg_transform is not formrdesc'd — only pg_database/pg_authid/
pg_auth_members/pg_shseclabel/pg_subscription are formrdesc'd shared rels
per `postgres/src/backend/utils/cache/relcache.c:4075-4083`, so the
Phase3 `relation->rd_att->tdtypeid == relp->reltype` assertion at
`relcache.c:4293` does not fire on pg_transform.

### (b) `nailedLocalRels` (relcache_init.go) gains

- heap entry `{3576, "pg_transform", 83, 'r', 5, false, pgTransformAttrs()}`
  after the Step 3cg pg_subscription_rel entry
- idxSpec entries `{3574, "pg_transform_oid_index"}` and
  `{3575, "pg_transform_type_lang_index"}` after the Step 3cg 6117 entry

### (c) `pgIndexInitialEntries` local section (initdb.go) gains

- `entry(3574, 3576, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)`
- `entry(3575, 3576, []int16{2, 3}, []uint32{oidOps, oidOps}, []uint32{0, 0}, true, false)`

after the Step 3cg 6117 entry. Heap attnums: 1=oid, 2=trftype, 3=trflang.

### (d) Critical index placeholder OID lists

Both blocks in `bootstrapPostgresDatabase` (`base/<dboid>/` + `global/`
fallback) gain `3574` and `3575` after the Step 3ch 2698 entry.

### (e) `bootstrapMappedLocalCatalogHeaps` + `localRelMap`

Both gain `3576` (authoritative pg_transform OID per `pg_transform.h:29`).
The pre-existing stale `6137 // pg_transform` placeholder is left in
place — 6137 has no upstream catalog assignment so the empty 8 KiB heap
at `base/{1,5}/6137` is harmless — and its comment is updated to flag the
historical mislabel.

### (f) Type helpers — no change required

`oid` (26) and `regproc` (24) are already registered in
`pgCatalogTypeOID` / `pgCatalogTypeLen` / `pgTypeByVal` /
`pgTypeAlignChar` / `pgTypeStorageChar`.

## Regression pins

New tests in `internal/initdb/pg_transform_nailed_test.go`:

- `TestNailedLocalRelsContainsPgTransform` — pin heap entry + every column
  descriptor
- `TestNailedLocalRelsContainsPgTransformIndexes` — pin both index entries
  with correct RelKind/RelNatts
- `TestPgTransformIndexInitialEntries` — pin IndKey/IndClass/IndCollation/
  IsUnique/IsPrimary for both indexes against the PG18 declarations
- `TestPgTransformAttrsTypeOIDsMatchPG18` — pin exact TypeOID/Len/NotNull

Existing tests extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` map gains `3574:{1}` and
  `3575:{2,3}` (strict count guard forces every future addition to update
  this)
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  gains 3574 and 3575

## Verification

- `go build ./...` PASS
- targeted `go test -count=1 -run 'TestNailedLocalRelsContainsPgTransform|
  TestPgTransformIndexInitialEntries|TestPgTransformAttrsTypeOIDsMatchPG18|
  TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|
  TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|
  TestNailedIndexRelnattsAgreesWithIndnatts|TestPgClassOidIndexHasSingleKeyColumn|
  TestPgIndexColDefsMatchesRelcacheAttrs' ./internal/initdb/` PASS
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3ch (TestMigrationFromLegacyJSONCluster,
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
  3576 is closed; the next standby-side FATAL is `could not open relation
  with OID 3603` (pg_ts_config_map per `pg_ts_config_map.h:30`), which is
  Step 3cj territory.

## Files changed

- `internal/initdb/relcache_init.go` — heap entry, idxSpec entries, and
  new `pgTransformAttrs()` function
- `internal/initdb/initdb.go` — `pgIndexInitialEntries` local section,
  `bootstrapMappedLocalCatalogHeaps` oids list, `localRelMap`, both
  critical-index placeholder OID lists
- `internal/initdb/pg_transform_nailed_test.go` — new pin file
- `internal/initdb/pg_index_indkey_test.go` — map gains 3574 + 3575
- `internal/initdb/btree_index_bootstrap_test.go` — mustHave gains 3574
  + 3575
