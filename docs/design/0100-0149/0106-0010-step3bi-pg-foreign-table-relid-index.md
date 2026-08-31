# M0106-0010 Step 3bi — Seed `pg_foreign_table_relid_index` (OID 3119)

## Status

LANDED 2026-05-18.

## Problem

Re-running `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
after Step 3bh seeded the `pg_foreign_table` heap nailed rel (OID 3118)
surfaces the next standby-boot blocker:

```
FATAL:  could not open relation with OID 3119
```

OID 3119 is `pg_foreign_table_relid_index` per
`postgres/src/include/catalog/pg_foreign_table_d.h:24`
(`#define ForeignTableRelidIndexId 3119`).

## Root cause

PG18's `pg_foreign_table.h:47` declares:

```c
DECLARE_UNIQUE_INDEX_PKEY(pg_foreign_table_relid_index, 3119,
    ForeignTableRelidIndexId, pg_foreign_table,
    btree(ftrelid oid_ops));
MAKE_SYSCACHE(FOREIGNTABLEREL, pg_foreign_table_relid_index, 4);
```

`pg_foreign_table_relid_index` backs the `FOREIGNTABLEREL` syscache.
Once Step 3bh seeded the heap (3118), PG's `RelationCacheInitFile` /
`load_relcache_init_file` flow walks the FDW catalog cluster and tries to
open 3119; without a `Form_pg_class` row, `RelationBuildDesc(3119) →
ScanPgRelation(3119)` returns NULL and the backend FATALs.

## Fix

Pure catalog-seed change mirroring the oid-PKEY pattern of Steps 3bd
(`pg_foreign_data_wrapper_oid_index` 112) and 3bg
(`pg_foreign_server_oid_index` 113). No encoder/builder/`Init` flow
change.

1. `internal/initdb/initdb.go::pgIndexInitialEntries` appends after the
   Step 3bg entry:
   ```go
   entry(3119, 3118, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)
   ```
   - `IndRelid=3118`: heap is `pg_foreign_table` (Step 3bh nailed rel).
   - `IndKey=[1]`: `Anum_pg_foreign_table_ftrelid = 1`
     (`pg_foreign_table_d.h:31`).
   - `oidOps`: btree opclass on `oid`.
   - `IndCollation=[0]`: oid_ops has no collation.
   - `IsUnique=true, IsPrimary=true`: `DECLARE_UNIQUE_INDEX_PKEY`.
2. `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec list gains
   `{3119, "pg_foreign_table_relid_index"}` after the Step 3bg entry.
   `flattenRels` + `pgIndexNattsByOID()` derives `RelKind='i', RelNatts=1`
   so PG's `RelationInitIndexAccessInfo` `relnatts == indnatts` check
   (relcache.c:1492) passes.
3. Three empty-placeholder OID lists at `bootstrapPostgresDatabase`
   (`base/1/`, `base/5/`, `global/`) gain `3119` so PG's `mdopen` finds a
   valid empty-btree file before `bootstrapPgIndexIndexrelidIndex`
   overwrites the metapage. The Step-3k empty btree is sufficient because
   `pg_foreign_table` is currently unpopulated (no foreign tables have
   been created via DDL).

The seed flows automatically through `bootstrapPgClassTuples` →
`bootstrapPgAttributeTuples` → `bootstrapPgIndexTuples` (writes
`Form_pg_index` row + captures TID in `pgIndexTIDs[3119]`) →
`bootstrapPgIndexIndexrelidIndex` (adds leaf to populated 2-page btree
at file 2679) → `bootstrapPgClassOidIndex` (leaf at file 2662) →
`bootstrapPgAttributeRelidAttnumIndex` (composite-key leaf at file 2659).

## Special note on attnum 1

Unlike most system catalogs, `pg_foreign_table` has no system `oid`
column — `ftrelid` (also of type `oid`, referencing `pg_class.oid`) is
the primary key. PG18's `pg_foreign_table_d.h` defines
`Anum_pg_foreign_table_ftrelid = 1`, so `IndKey=[1]` still encodes the
correct heap attnum. The on-disk btree leaf format is identical to other
`oid_ops`-keyed indexes (4-byte oid + ItemPointer).

## Tests

- New file `internal/initdb/pg_foreign_table_relid_index_test.go`:
  - `TestPgForeignTableRelidIndexSeededFromInitialEntries` — pins
    `(IndRelid=3118, IndKey=[1], IsUnique=true, IsPrimary=true,
    IndCollation=[0])` against `pg_foreign_table.h:47`.
  - `TestNailedLocalRelsContainsPgForeignTableRelidIndex` — pins
    `RelName="pg_foreign_table_relid_index", RelKind='i', RelNatts=1`.
- Existing pins extended:
  - `TestPgIndexInitialEntriesIndkeyMatchesPG18` map gains `3119:{1}`
    (strict count guard catches future adds without map updates).
  - `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
    extended with 3119 so the populated 2679 btree must carry the leaf.

## Verification

- `go build ./...` PASS.
- `go test -count=1 -run 'TestPgForeignTableRelidIndex|TestNailedLocalRelsContainsPgForeignTableRelidIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestNailedLocalRelsContainsPgForeignTable|TestBootstrapMappedLocalCatalogHeaps' ./internal/initdb/` PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3bh (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` PASS.
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
  re-run: standby advances past the `could not open relation with OID
  3119` FATAL to `could not open relation with OID 2681`
  (`pg_index_indrelid_index`) — Step 3bj territory.
