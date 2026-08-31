# M0106-0010 Step 3bb — pg_foreign_data_wrapper nailed local rel

## Blocker

After Step 3ba landed the firstright HIKEY pivot for multi-leaf btrees,
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` advanced
past the multi-leaf btree assertion but the next backend opened by the PG
standby FATAL'd with:

```
FATAL: could not open relation with OID 2328
```

OID 2328 is `pg_foreign_data_wrapper`, NOT `pg_db_role_setting` as the
Step 3ba note speculated. Authoritative source:

* `postgres/src/include/catalog/pg_foreign_data_wrapper.h` line 29:
  `CATALOG(pg_foreign_data_wrapper,2328,ForeignDataWrapperRelationId)`
* `postgres/src/include/catalog/pg_foreign_data_wrapper_d.h` line 23:
  `#define ForeignDataWrapperRelationId 2328`

The FATAL is emitted from
`postgres/src/backend/access/common/relation.c:61` because
`RelationBuildDesc(2328) → ScanPgRelation(2328)` returned NULL —
goopg's `localRelMap` did not advertise the relation, no `pg_class` row
existed for OID 2328, and no heap file existed at `base/{1,5}/2328`.

## Fix

Pure catalog-seed change. No encoder, builder, or `Init` flow change.
Same pattern as Steps 3w (pg_aggregate=2600), 3aa (pg_cast=2605),
3ag (pg_conversion=2607), 3ak (pg_default_acl=826),
3an (pg_enum=3501), 3ar (pg_event_trigger=3466), and
3aw (pg_extension=3079).

### 1. `internal/initdb/relcache_init.go`

* New `pgForeignDataWrapperAttrs()` returns the 7-column PG18 schema
  verbatim from `pg_foreign_data_wrapper.h` /
  `pg_foreign_data_wrapper_d.h`:
  * `oid` (TypeOID 26, Len 4, NOT NULL)
  * `fdwname` (TypeOID 19 name, Len 64, NOT NULL)
  * `fdwowner` (TypeOID 26 → pg_authid, Len 4, NOT NULL)
  * `fdwhandler` (TypeOID 26 → pg_proc opt, Len 4, NOT NULL)
  * `fdwvalidator` (TypeOID 26 → pg_proc opt, Len 4, NOT NULL)
  * `fdwacl` (TypeOID 1034 aclitem[], Len -1, nullable)
  * `fdwoptions` (TypeOID 1009 text[], Len -1, nullable)

  The two trailing varlena columns sit in the CATALOG_VARLEN block with
  no `BKI_FORCE_NOT_NULL` attribute, so they are nullable. The heap is
  empty at bootstrap time, so the encoder is not exercised.

* `nailedLocalRels` gains
  `{2328, "pg_foreign_data_wrapper", 83, 'r', 7, false, pgForeignDataWrapperAttrs()}`
  immediately after the Step 3aw pg_extension entry. `RelType=83` is
  safe because pg_foreign_data_wrapper is not formrdesc'd (no
  `ForeignDataWrapperRelation_Rowtype_Id` constant in PG18 headers), so
  Step 3v's `relation->rd_att->tdtypeid == relp->reltype` Phase-3
  assertion does not fire.

### 2. `internal/initdb/initdb.go`

* `bootstrapMappedLocalCatalogHeaps` OID list gains `2328`. An empty 8 KiB
  `InitPage`-stamped heap is sufficient because no foreign-data-wrapper
  rows are bootstrapped — any `SearchSysCache1(FOREIGNDATAWRAPPER{OID,NAME}, …)`
  probe correctly returns no row.
* `localRelMap` gains `{2328, 2328}` so PG's relfilenode mapper resolves
  OID 2328 to a backing file.

### Threading

The nailed-rel entry threads automatically through the existing
bootstrap flow:

* `bootstrapPgClassTuples` writes the `Form_pg_class` row for OID 2328.
* `bootstrapPgAttributeTuples` writes 7 `pg_attribute` rows.
* `bootstrapPgClassOidIndex` adds a leaf for 2328 to `2662`.
* `bootstrapPgAttributeRelidAttnumIndex` adds 7 composite-key leaves to
  `2659`.
* `writeRelcacheInitFile` emits a `Form_pg_class` + 7 `Form_pg_attribute`
  blob group.

## Companion indexes (deferred)

`pg_foreign_data_wrapper.h` lines 57-58 declare two indexes:

* OID 112 = `pg_foreign_data_wrapper_oid_index`
  (UNIQUE PRIMARY KEY on `oid oid_ops`,
  backs `MAKE_SYSCACHE(FOREIGNDATAWRAPPEROID, …)`)
* OID 548 = `pg_foreign_data_wrapper_name_index`
  (UNIQUE on `fdwname name_ops`,
  backs `MAKE_SYSCACHE(FOREIGNDATAWRAPPERNAME, …)`)

Both are intentionally deferred until concrete blockers surface in the
next E2E re-runs. This preserves the single-OID rhythm of Steps 3w → 3aa
→ 3ag → 3ak → 3an → 3ar → 3aw.

## Regression pins

* `TestNailedLocalRelsContainsPgForeignDataWrapper` —
  full per-column `(Name, TypeOID, Num, Len, NotNull)` audit, asserts
  `RelKind='r', RelNatts=7, len(Attrs)=7`.
* `TestBootstrapMappedLocalCatalogHeapsIncludesPgForeignDataWrapper` —
  asserts `base/{1,5}/2328` exists, is exactly 8 KiB, and is not
  all-zero (InitPage applied).
* `TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages::wantOIDs`
  extended with 2328 so the placeholder list cannot silently drop
  pg_foreign_data_wrapper.

## Verification

* `go build ./...` PASS.
* Targeted: `go test -count=1 -run
  'TestNailedLocalRelsContainsPgForeignDataWrapper|TestBootstrapMappedLocalCatalogHeapsIncludesPgForeignDataWrapper|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestNailedLocalRelsContainsPgExtension|TestNailedLocalRelsContainsPgEnum|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexInitialEntriesIndkeyMatchesPG18'
  ./internal/initdb/` PASS.
* Full: `go test -count=1 ./internal/initdb/` — same 14 pre-existing
  baseline failures as Step 3ba (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestOpenOldClusterWithoutM0030FilesStillWorks`,
  `TestSynchronousCommitFlushesByDefault`) — no new regressions.
* Cross-package smoke: `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` PASS.

## Next blocker

The next E2E re-run is expected to surface either the FDW companion
indexes (OID 112 or 548) or another `pg_*` heap OID flagged by
`RelationCacheInitializePhase3`'s nailed-rel walk. Same single-OID
catalog-seed-addition pattern applies.
