# M0106-0010 Step 3bn — `pg_opfamily_am_name_nsp_index` catalog seed

## Problem

`TestE2E_FailoverGoopgToPG/async` (with
`GOOPG_RUN_BLOCKED_M0102_E2E=1`) FATALs on every backend the PG
standby's postmaster forks after Step 3bm added `pg_opfamily`
(heap OID 2753) to `nailedLocalRels`:

```
FATAL:  could not open relation with OID 2754
```

PG's `RelationIdGetRelation(2754) → ScanPgRelation(2754)` returns NULL
because no `pg_class` row is seeded for OID 2754, so
`load_relation_oid` emits the FATAL at
`postgres/src/backend/access/common/relation.c:61`.

## Authoritative source

`postgres/src/include/catalog/pg_opfamily.h:47`:

```c
DECLARE_UNIQUE_INDEX(pg_opfamily_am_name_nsp_index, 2754,
    OpfamilyAmNameNspIndexId, pg_opfamily,
    btree(opfmethod oid_ops, opfname name_ops,
          opfnamespace oid_ops));

MAKE_SYSCACHE(OPFAMILYAMNAMENSP, pg_opfamily_am_name_nsp_index, 8);
```

- UNIQUE but **NOT** primary — `DECLARE_UNIQUE_INDEX` not the `_PKEY`
  variant. The PKEY of `pg_opfamily` is OID 2755
  (`pg_opfamily_oid_index`, deferred to Step 3bo).
- 3-column composite key. `opfname` is a `name`-typed column whose
  `name_ops` btree opclass carries `C_COLLATION_OID = 950`, same
  convention as Steps 3ad / 3aj / 3ae / 3bj.

`pg_opfamily` attnums per `pg_opfamily.h` (matches goopg's
`pgOpfamilyAttrs()` from Step 3bm): 1=oid, 2=opfmethod, 3=opfname,
4=opfnamespace, 5=opfowner.

## Fix

Pure catalog-seed addition mirroring the composite UNIQUE non-PKEY
pattern of Step 3ad (`pg_opclass_am_name_nsp_index` 2686), Step 3aj
(`pg_conversion_name_nsp_index` 2669), and Step 3ae
(`pg_collation_name_enc_nsp_index` 3164). No encoder, builder, or
`Init` flow change.

1. `internal/initdb/initdb.go::pgIndexInitialEntries` gains
   `entry(2754, 2753, []int16{2, 3, 4},
   []uint32{oidOps, nameOps, oidOps},
   []uint32{0, cCollation, 0}, true, false)` after the Step 3bl
   2689 entry.

2. `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec list
   gains `{2754, "pg_opfamily_am_name_nsp_index"}` after the 2689
   entry. `flattenRels` consults `pgIndexNattsByOID()` (returns 3)
   so the nailed rel carries `RelKind='i', RelNatts=3`, satisfying
   `RelationInitIndexAccessInfo`'s `relnatts == indnatts` check
   (`relcache.c:1492`).

3. Three empty-placeholder OID lists in `bootstrapPostgresDatabase`
   (`base/1/`, `base/5/`, `global/`) gain
   `2754 // pg_opfamily_am_name_nsp_index (Step 3bn)` so PG's
   `mdopen` finds a valid empty-btree file (Step-3k placeholder
   metapage with `btm_root = P_NONE`) before
   `bootstrapPgIndexIndexrelidIndex` overwrites the metapage with the
   populated 2-page btree leaf carrying this OID's `Form_pg_index`
   TID. The empty-btree placeholder is functionally sufficient because
   `pg_opfamily` is currently unpopulated — any
   `SearchSysCache3(OPFAMILYAMNAMENSP, …)` probe correctly returns
   no row.

## Flow

The seed threads automatically through:

```
bootstrapPgClassTuples         → writes Form_pg_class row for 2754
bootstrapPgAttributeTuples     → writes 3 indexKeyAttrs rows
bootstrapPgIndexTuples         → writes Form_pg_index row, captures TID
bootstrapPgIndexIndexrelidIndex → adds leaf at file 2679
bootstrapPgClassOidIndex        → adds leaf at file 2662
bootstrapPgAttributeRelidAttnumIndex → adds 3 composite-key leaves at file 2659
```

## Tests

New file `internal/initdb/pg_opfamily_am_name_nsp_index_test.go`:

- `TestPgOpfamilyAmNameNspIndexSeededFromInitialEntries` — pins
  `(IndRelid=2753, IndKey=[2 3 4], IsUnique=true, IsPrimary=false,
  IndCollation=[0 950 0])`.
- `TestNailedLocalRelsContainsPgOpfamilyAmNameNspIndex` — pins
  `RelName, RelKind='i', RelNatts=3`.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` map gains
  `2754: {2, 3, 4}` (strict count guard catches future adds without
  map updates).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  list extended with 2754 so the populated 2679 btree must carry
  this leaf.

## Verification

- `go build ./...` PASS.
- Targeted: `go test -count=1 -run
  'TestPgOpfamilyAmNameNspIndex|TestNailedLocalRelsContainsPgOpfamilyAmNameNspIndex|TestNailedLocalRelsContainsPgOpfamily|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestBootstrapMappedLocalCatalogHeapsIncludesPgOpfamily|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages|TestPgOperatorOprnameLRNIndex'
  ./internal/initdb/` PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing
  baseline failures as Step 3bm (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` PASS.

## Next step

Step 3bo: companion `pg_opfamily_oid_index` (OID 2755, UNIQUE PRIMARY
KEY on `oid_ops`, backs `MAKE_SYSCACHE(OPFAMILYOID, …)`) once the next
E2E re-run surfaces the corresponding FATAL.
