# M0106-0010 Step 3cp — Seed `pg_user_mapping` (OID 1418) + indexes 174 / 175

## Goal

Close the FATAL `could not open relation with OID 1418` PG-standby boot
blocker that surfaces immediately after Step 3co seeded the
`pg_ts_template` family (OID 3764). `pg_user_mapping` is the foreign-
data-wrapper user-mapping catalog. It is opened during PG-standby boot
via `RelationCacheInitializePhase3 →
load_critical_index(USERMAPPINGOID, ...)` (the
`load_critical_index` → `RelationBuildDesc(1418)` path drives a
`ScanPgRelation(1418)` lookup that returns NULL when no pg_class row
exists).

## Authoritative Upstream Reference

`postgres/src/include/catalog/pg_user_mapping.h`

```c
CATALOG(pg_user_mapping,1418,UserMappingRelationId)
{
    Oid         oid;                                /* oid */
    Oid         umuser BKI_LOOKUP_OPT(pg_authid);   /* InvalidOid = PUBLIC */
    Oid         umserver BKI_LOOKUP(pg_foreign_server);

#ifdef CATALOG_VARLEN
    text        umoptions[1];                       /* nullable */
#endif
} FormData_pg_user_mapping;

DECLARE_UNIQUE_INDEX_PKEY(pg_user_mapping_oid_index, 174,
    UserMappingOidIndexId, pg_user_mapping, btree(oid oid_ops));
DECLARE_UNIQUE_INDEX(pg_user_mapping_user_server_index, 175,
    UserMappingUserServerIndexId, pg_user_mapping,
    btree(umuser oid_ops, umserver oid_ops));

MAKE_SYSCACHE(USERMAPPINGOID, pg_user_mapping_oid_index, 2);
MAKE_SYSCACHE(USERMAPPINGUSERSERVER, pg_user_mapping_user_server_index, 2);
```

Per-database (non-shared) catalog. 4-column schema: three fixed-width
NOT NULL `oid` columns (`oid`, `umuser`, `umserver`) plus one
`CATALOG_VARLEN text[]` (`umoptions`, typeoid 1009 = `_text`, nullable).
`umuser` uses `BKI_LOOKUP_OPT` so InvalidOid (0) means PUBLIC; the
column itself remains NOT NULL.

Note the deliberately low index OIDs — 174 / 175 are upstream pinned
OIDs from when pg_user_mapping was first added in PG 8.4. They are
NOT typos.

## Implementation

### (a) `pgUserMappingAttrs()` (`internal/initdb/relcache_init.go`)

4-column descriptor returning the verbatim PG18 schema:

| attnum | name      | typeoid | typlen | notnull |
|--------|-----------|---------|--------|---------|
| 1      | oid       | 26      | 4      | true    |
| 2      | umuser    | 26      | 4      | true    |
| 3      | umserver  | 26      | 4      | true    |
| 4      | umoptions | 1009    | -1     | false   |

### (b) `nailedLocalRels` extension (`relcache_init.go`)

Heap list gains `{1418, "pg_user_mapping", 83, 'r', 4, false,
pgUserMappingAttrs()}` after the Step 3co `{3764, "pg_ts_template", …}`
entry. `RelType=83` is safe — no `UserMappingRelation_Rowtype_Id`
constant in PG18 headers, so the Phase3
`relation->rd_att->tdtypeid == relp->reltype` assertion does not fire
(formrdesc'd shared rels are listed at relcache.c:4075-4083).

Index list gains two `idxSpec` rows:

- `{174, "pg_user_mapping_oid_index"}` — UNIQUE PRIMARY single-column
  oid_ops on attnum 1; backs `USERMAPPINGOID` syscache.
- `{175, "pg_user_mapping_user_server_index"}` — UNIQUE (NOT PRIMARY)
  2-column composite (`umuser`, `umserver`) both oid_ops, no
  collation; backs `USERMAPPINGUSERSERVER` syscache.

`flattenRels` derives RelNatts via `pgIndexNattsByOID()` so the
nailed rel's `RelNatts` automatically matches `indnatts` from
`pgIndexInitialEntries` (relcache.c:1492 consistency check).

### (c) `pgIndexInitialEntries()` (`internal/initdb/initdb.go`)

Two new `entry()` calls appended after the Step 3co
pg_ts_template_oid_index row:

```go
entry(174, 1418, []int16{1},   []uint32{oidOps},         []uint32{0},    true, true)
entry(175, 1418, []int16{2,3}, []uint32{oidOps, oidOps}, []uint32{0, 0}, true, false)
```

### (d) `bootstrapMappedLocalCatalogHeaps` (`initdb.go`)

`oids` slice gains `1418` immediately after the Step 3be 1417
(pg_foreign_server) entry so PG's `mdopen("base/<db>/1418")` finds an
`InitPage`-stamped empty 8 KiB heap page. Since no real
user mappings are bootstrapped, this empty heap is the canonical state
PG observes.

### (e) `bootstrapPostgresDatabase` (`initdb.go`)

Three placeholder lists updated:

1. `localRelMap` (filenode map): adds `{1418, 1418}` after the
   1417 entry so PG can resolve OID 1418 → file 1418 in
   `base/<dboid>/pg_filenode.map`.
2. dbDir (`base/5/`) critical-index list: adds `174` and `175`
   so PG's early critical-index loader finds an `InitPage`-stamped
   empty btree metapage (`btm_root=P_NONE` is sufficient because
   pg_user_mapping is unpopulated).
3. global (`global/`) critical-index list: adds `174` and `175`
   (defensive copy — some PG code paths use `InvalidOid` as dbNode
   for nailed relations, causing lookups in `global/`).

The `base/1/` list at line 699-735 is deliberately NOT updated;
recent steps (3bx onwards) skip `base/1/` because PG-standby boot
only connects to `base/5/` (postgres). Files in `base/5/` get copied
in via the `entries, _ := os.ReadDir(base1Dir)` loop *before* the
dbDir-specific writes happen, so the dbDir-only entries 174/175 land
in `base/5/` correctly.

### (f) Type-helper registration

No new type-helper entries needed. All four types (`oid` 26,
`text[]` 1009) are already registered in `pgCatalogTypeOID` /
`pgCatalogTypeLen` / `pgTypeByVal` / `pgTypeAlignChar` /
`pgTypeStorageChar` (text[] was wired in Step 1 for pg_class.relacl
which is also `_aclitem` / `_text` family).

## Why this clears the FATAL

PG's standby-boot relcache initialisation drives
`RelationIdGetRelation(1418)` →
`RelationBuildDesc(1418)` → `ScanPgRelation(1418, indexOK=true)`.
After Step 3o flipped `criticalRelcachesBuilt = true`, this scan goes
through `pg_class_oid_index` (OID 2662) which Step 3m populated with
one leaf tuple per nailed rel in `nailedLocalRels`. Adding 1418 to
`nailedLocalRels` writes the corresponding heap pg_class row via
`bootstrapPgClassTuples` and its TID flows into the 2662 btree via
the existing `pgClassTIDs` plumbing.

`load_critical_index(USERMAPPINGOID, …)` also requires both 174 and
175 to have valid empty btree metapage files (PG's `_bt_getmeta` at
nbtpage.c:152 FATALs unless block 0 is a metapage carrying
`BTREE_MAGIC` and `BTP_META`); the Step-3k `makeBtreeRootPage`
placeholder provides exactly this layout.

Neither index needs populated leaves yet — pg_user_mapping is empty
on a fresh standby, so the `SearchSysCache1(USERMAPPINGOID, …)` and
`SearchSysCache2(USERMAPPINGUSERSERVER, …)` lookups returning zero
rows is the correct, expected behaviour.

## Regression Tests

New file `internal/initdb/pg_user_mapping_nailed_test.go`:

- `TestNailedLocalRelsContainsPgUserMapping` — pins the heap entry
  for OID 1418 plus every column descriptor (Name, TypeOID, Num,
  Len, NotNull). Catches accidental drops of any of the 4 columns
  and accidental NotNull flips on `umoptions`.
- `TestNailedLocalRelsContainsPgUserMappingIndexes` — pins the two
  index entries (174 / 175) and verifies `RelKind='i'` and
  `RelNatts` (1 / 2).
- `TestPgUserMappingIndexInitialEntries` — pins the
  `pgIndexInitialEntries` rows for 174 / 175, including IndRelid,
  IndKey, IsUnique, IsPrimary.

Existing tests extended:

- `internal/initdb/pg_index_indkey_test.go::TestPgIndexInitialEntriesIndkeyMatchesPG18`
  gains `174:{1}` and `175:{2,3}`. The strict count guard
  (`len(got) != len(want)` Fatal) auto-rejects future additions
  without map updates.
- `internal/initdb/btree_index_bootstrap_test.go::TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree`
  `mustHave` list gains 174 and 175 so the populated 2679
  btree must include both entries (catches accidental drops from
  `pgIndexInitialEntries`).

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestNailedLocalRelsContainsPgUserMapping|TestPgUserMappingIndexInitialEntries|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestNailedLocalRelsContainsPgTsTemplate'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing
  baseline failures as Step 3co (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`). No new regressions.
- Cross-package smoke
  `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Next Blocker

The next FATAL on PG-standby boot will surface the next missing
catalog OID. After pg_user_mapping (1418), the natural candidates in
the relcache-load order are remaining unseeded local rels visible to
`RelationCacheInitializePhase3`'s nailed-rel sweep — to be
determined by the next `TestE2E_FailoverGoopgToPG/async` reproduction.
