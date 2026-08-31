# M0106-0010 Step 3cm — pg_ts_dict nailed local rel + indexes 3604/3605

Status: accepted
Date: 2026-05-18

## Problem

After Step 3ck cleared the pg_ts_config family (OID 3602 plus indexes
3608/3712), PG-standby boot's next FATAL is `could not open relation
with OID 3600`. OID 3600 is `pg_ts_dict` per
`postgres/src/include/catalog/pg_ts_dict.h:29`
(`CATALOG(pg_ts_dict,3600,TSDictionaryRelationId)`). Without a pg_class
row, `RelationBuildDesc(3600) → ScanPgRelation(3600)` returns NULL and
the backend dies.

Per-database (non-shared) catalog. PG18 declares two unique indexes on
`pg_ts_dict`:

```
DECLARE_UNIQUE_INDEX(pg_ts_dict_dictname_index, 3604,
    TSDictionaryNameNspIndexId, pg_ts_dict,
    btree(dictname name_ops, dictnamespace oid_ops));
DECLARE_UNIQUE_INDEX_PKEY(pg_ts_dict_oid_index, 3605,
    TSDictionaryOidIndexId, pg_ts_dict,
    btree(oid oid_ops));
```

backing `MAKE_SYSCACHE(TSDICTNAMENSP, …, 2)` and
`MAKE_SYSCACHE(TSDICTOID, …, 2)` respectively. Closing the heap FATAL
without also nailing both indexes would just relocate the FATAL onto
`RelationIdGetRelation(3604)` next, so this step seeds the full family.

## Schema (PG18 `pg_ts_dict.h:29-50` + `pg_ts_dict_d.h`)

`Natts_pg_ts_dict == 6`. pg_ts_dict DOES carry an `oid` system column —
attnums start at 1 = oid. Columns 1..5 are fixed-width NOT NULL;
column 6 is a CATALOG_VARLEN NULLABLE text.

| attnum | name           | typoid | len | not null | notes |
|--------|---------------|--------|-----|----------|-------|
| 1      | oid            | 26     | 4   | yes      | system OID |
| 2      | dictname       | 19     | 64  | yes      | NameData, BKI_DEFAULT |
| 3      | dictnamespace  | 26     | 4   | yes      | BKI_LOOKUP pg_namespace |
| 4      | dictowner      | 26     | 4   | yes      | BKI_LOOKUP pg_authid |
| 5      | dicttemplate   | 26     | 4   | yes      | BKI_LOOKUP pg_ts_template |
| 6      | dictinitoption | 25     | -1  | no       | text, CATALOG_VARLEN |

RelType=83 is safe (no `TSDictionaryRelation_Rowtype_Id` constant in
PG18 headers — only pg_database/pg_authid/pg_auth_members/
pg_shseclabel/pg_subscription are formrdesc'd shared rels at
`postgres/src/backend/utils/cache/relcache.c:4075-4083`).

## Changes

### `internal/initdb/relcache_init.go`

- New `pgTsDictAttrs()` returns the 6-column descriptor list above.
- `nailedLocalRels` heap list gains
  `{3600, "pg_ts_dict", 83, 'r', 6, false, pgTsDictAttrs()}` after the
  Step 3ck pg_ts_config entry.
- `idxSpec` list gains `{3604, "pg_ts_dict_dictname_index"}` and
  `{3605, "pg_ts_dict_oid_index"}` after the Step 3ck 3712 entry.

### `internal/initdb/initdb.go`

- `pgIndexInitialEntries` local section gains
  `entry(3604, 3600, []int16{2, 3}, []uint32{nameOps, oidOps},
  []uint32{cCollation, 0}, true, false)` and
  `entry(3605, 3600, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)`
  after the Step 3ck 3712 entry. `dictname` uses C_COLLATION_OID because
  PG's catalog `name` columns are always C-collation; `dictnamespace`
  carries no collation.
- Both "Critical index placeholder pages" OID lists
  (`base/<dboid>/` block + `global/` fallback block) at
  `bootstrapPostgresDatabase` gain 3604 and 3605 after the Step 3ck
  3712 entry.
- `bootstrapMappedLocalCatalogHeaps` `oids` slice and `localRelMap`
  gain 3600 (the authoritative OID per `pg_ts_dict.h:29`). The
  pre-existing stale `3766` placeholder (commented as "pg_ts_dict" —
  3766 has no upstream catalog assignment) is left in place; its
  comment is updated to flag the historical mislabel and reaffirm
  that the canonical OID is 3600. This mirrors the conservative
  approach taken in Step 3ci (3576/6137), Step 3cj (3603/3765), and
  Step 3ck (3602/3764).
- No new type-helper entries needed: oid (26), name (19), text (25)
  are all already registered in
  `pgCatalogTypeOID` / `pgCatalogTypeLen` / `pgTypeByVal` /
  `pgTypeAlignChar` / `pgTypeStorageChar`.

### Regression pins

New file `internal/initdb/pg_ts_dict_nailed_test.go`:

- `TestNailedLocalRelsContainsPgTsDict` — heap entry shape and all 6
  attr descriptors.
- `TestNailedLocalRelsContainsPgTsDictIndexes` — both index entries
  (3604 / 3605) by name + relnatts.
- `TestPgTsDictIndexInitialEntries` — index columns / opclass /
  collation / unique / primary flags.
- `TestPgTsDictAttrsTypeOIDsMatchPG18` — typoids + lens + nullability
  for every column.

Existing pin extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` map gains
  `3604:{2,3}` and `3605:{1}` (the strict count guard at the bottom
  of the test would otherwise FAIL with an "entries, want N" mismatch).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  gains 3604 and 3605.

## Verification

- `go build ./...` — PASS.
- Targeted suite —
  `go test -count=1 -run 'TestNailedLocalRelsContainsPgTsDict|TestPgTsDictIndexInitialEntries|TestPgTsDictAttrsTypeOIDsMatchPG18|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgClassOidIndexHasSingleKeyColumn|TestPgIndexColDefsMatchesRelcacheAttrs' ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3ck (`TestMigrationFromLegacyJSONCluster`,
  `TestMigrationIdempotent`, `TestMigrationPGAttributeRowsWritten`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestCreateTableSurvivesRestartViaCatalogHeap`,
  `TestMultipleTablesLoadFromHeap`,
  `TestCreateIndexSurvivesRestartViaWAL`,
  `TestCreateIndexRecoveredOIDDoesNotCollide`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestBootstrappedPGClassRowsReadable`,
  `TestBootstrappedPGAttributeRowsReadable`,
  `TestOpenOldClusterWithoutM0030FilesStillWorks`,
  `TestSynchronousCommitFlushesByDefault`). No new regressions.
- Cross-package smoke — `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.

## Follow-ups (not in this step)

- Step 3cn / 3co will seed pg_ts_parser (OID 3601) and pg_ts_template
  (OID 3764) — both per-database (non-shared) catalogs declared in
  `pg_ts_parser.h` and `pg_ts_template.h` respectively, each with two
  unique indexes. Same recipe as this step.
