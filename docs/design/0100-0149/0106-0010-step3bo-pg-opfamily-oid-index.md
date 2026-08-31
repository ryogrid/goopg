# M0106-0010 Step 3bo — `pg_opfamily_oid_index` catalog seed

## Problem

After Step 3bn seeded `pg_opfamily_am_name_nsp_index` (OID 2754),
`TestE2E_FailoverGoopgToPG/async` (with `GOOPG_RUN_BLOCKED_M0102_E2E=1`)
is expected to surface the next PG-standby boot FATAL:

```
FATAL:  could not open relation with OID 2755
```

PG's `RelationIdGetRelation(2755) → ScanPgRelation(2755)` returns NULL
because no `pg_class` row is seeded for OID 2755, so PG's relcache build
falls back to `formrdesc()` for the well-known catalog OIDs and FATALs
for anything else.

OID 2755 is `pg_opfamily_oid_index` per `postgres/src/include/catalog/pg_opfamily.h:54`:

```c
DECLARE_UNIQUE_INDEX_PKEY(pg_opfamily_oid_index, 2755,
    OpfamilyOidIndexId, pg_opfamily, btree(oid oid_ops));

MAKE_SYSCACHE(OPFAMILYOID, pg_opfamily_oid_index, 8);
```

This is the PRIMARY KEY of `pg_opfamily` (heap OID 2753, nailed by
Step 3bm). It backs the `OPFAMILYOID` syscache used by every
`SearchSysCache1(OPFAMILYOID, …)` probe in PG.

## Fix

Pure catalog-seed addition mirroring the single-column `oid_ops`
UNIQUE PKEY pattern of Steps:

- 3bk (`pg_language_oid_index`, OID 2682)
- 3l (`pg_opclass_oid_index`, OID 2687)
- 3ax (`pg_extension_oid_index`, OID 3080)
- 3at (`pg_event_trigger_oid_index`, OID 3468)
- 3bd (`pg_foreign_data_wrapper_oid_index`, OID 112)
- 3bg (`pg_foreign_server_oid_index`, OID 113)

No encoder, builder, or `Init` flow change.

### Changes

1. **`internal/initdb/initdb.go::pgIndexInitialEntries`** appends after
   the Step 3bn 2754 entry:

   ```go
   entry(2755, 2753, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_opfamily_oid_index
   ```

   `IndKey={1}` = `Anum_pg_opfamily_oid` (1-based, `oid` column).
   `oid_ops` carries no collation.

2. **`internal/initdb/relcache_init.go::nailedLocalRels`** idxSpec gains
   `{2755, "pg_opfamily_oid_index"}` after the Step 3bn 2754 entry.
   `flattenRels` + `pgIndexNattsByOID` derives `RelKind='i', RelNatts=1`
   so the `relnatts == indnatts` check (`relcache.c:1492`) passes.

3. **Three placeholder OID lists** at `bootstrapPostgresDatabase`
   (`base/1/`, `base/5/`, `global/`) gain
   `2755, // pg_opfamily_oid_index (Step 3bo)` immediately after the
   `2754` entry from Step 3bn. The Step-3k empty-btree placeholder is
   sufficient because pg_opfamily is currently unpopulated — any
   `SearchSysCache1(OPFAMILYOID, …)` probe correctly returns no row.

### Regression pins

- `TestPgOpfamilyOidIndexSeededFromInitialEntries` —
  pins `(IndRelid=2753, IndKey=[1], IsUnique=true, IsPrimary=true,
  IndCollation=[0])`.
- `TestNailedLocalRelsContainsPgOpfamilyOidIndex` —
  pins `RelName, RelKind='i', RelNatts=1`.
- `TestPgIndexInitialEntriesIndkeyMatchesPG18` map extended with
  `2755:{1}` (strict count guard prevents silent drops).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with `2755`.

Files: `internal/initdb/pg_opfamily_oid_index_test.go` (new),
`internal/initdb/pg_index_indkey_test.go`,
`internal/initdb/btree_index_bootstrap_test.go`.

## Verification

- `go build ./...` — PASS.
- Targeted tests
  (`TestPgOpfamilyOidIndex…|TestNailedLocalRelsContainsPgOpfamilyOidIndex|
  TestPgOpfamilyAmNameNspIndex|TestNailedLocalRelsContainsPgOpfamily|
  TestPgIndexInitialEntriesIndkeyMatchesPG18|
  TestBootstrapPgIndexIndexrelidIndex|
  TestNailedIndexRelnattsAgreesWithIndnatts|
  TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|
  TestPgClassOidIndexHasSingleKeyColumn|
  TestBootstrapMappedLocalCatalogHeapsIncludesPgOpfamily|
  TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|
  TestPgOperatorOprnameLRNIndex|TestPgLanguageOidIndex|
  TestPgLanguageNameIndex`) — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3bn (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- Cross-package smoke (`./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/`) — PASS.

## Why this is the right next step

With Step 3bm seeding the `pg_opfamily` heap (OID 2753) and Step 3bn
seeding the `OPFAMILYAMNAMENSP` syscache index (OID 2754), the PRIMARY
KEY index OID 2755 is the only outstanding pg_opfamily catalog object.
Both syscaches (`OPFAMILYAMNAMENSP` and `OPFAMILYOID`) are now
addressable; subsequent steps move on to the next blocker surfaced by
the E2E test.

## Follow-up

Re-run `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
to confirm the next FATAL OID and file Step 3bp accordingly.
