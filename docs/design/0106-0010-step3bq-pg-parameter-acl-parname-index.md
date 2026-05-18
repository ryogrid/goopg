# M0106-0010 Step 3bq — pg_parameter_acl_parname_index (OID 6246)

Status: Accepted (landed 2026-05-18)

## Problem

After Step 3bp seeded `pg_parameter_acl` (OID 6243, BKI_SHARED_RELATION)
as a nailed shared rel, the next `TestE2E_FailoverGoopgToPG/async`
(`GOOPG_RUN_BLOCKED_M0102_E2E=1`) re-run surfaced the next FATAL on the
PG standby:

```
FATAL: could not open relation with OID 6246
```

OID 6246 is `pg_parameter_acl_parname_index` per the authoritative PG18
source `postgres/src/include/catalog/pg_parameter_acl.h:53`:

```c
DECLARE_UNIQUE_INDEX(pg_parameter_acl_parname_index, 6246,
  ParameterAclParnameIndexId, pg_parameter_acl,
  btree(parname text_ops));
MAKE_SYSCACHE(PARAMETERACLNAME, pg_parameter_acl_parname_index, 4);
```

It is the UNIQUE (non-PKEY) backing index of the `PARAMETERACLNAME`
syscache — touched during every backend's `InitPostgres` ACL-cache
warm-up after the matching `pg_parameter_acl` heap is opened.

Although `bootstrapPostgresDatabase`'s global/ placeholder list already
provided a valid empty btree page for the relfile, no entry existed in
either `nailedSharedRels` or `pgIndexInitialEntries` — so
`bootstrapPgClassTuples` never wrote a `Form_pg_class` row for OID 6246
and `RelationIdGetRelation(6246)` FATAL'd before any heap or btree page
was touched.

## Fix

Pure catalog-seed addition; no encoder, builder, or `Init` flow change.

1. **`internal/initdb/initdb.go::pgIndexInitialEntries`** — append to the
   `shared` slice (alongside the existing `2671/2672/2676/2677/2694/2695/3593`
   entries):

   ```go
   entry(6246, 6243, []int16{2}, []uint32{textOps}, []uint32{cCollation}, true, false)
   ```

   - `IndRelid = 6243` (pg_parameter_acl heap OID).
   - `IndKey = [2]` (parname is attnum 2 per `pg_parameter_acl_d.h`).
   - `IndClass = [textOps=3126]`, `IndCollation = [950]`
     (C_COLLATION_OID, required for text_ops).
   - `IsUnique = true`, `IsPrimary = false` —
     `DECLARE_UNIQUE_INDEX`, not the `_PKEY` variant (PKEY is OID 6247).

2. **`internal/initdb/relcache_init.go::nailedSharedRels`** — append
   `{6246, "pg_parameter_acl_parname_index"}` to the `idxSpec` list.
   `flattenRels` consults `pgIndexNattsByOID()` (returns 1 for OID 6246),
   so the nailed rel carries `RelKind='i', RelNatts=1`, and
   `RelationInitIndexAccessInfo`'s `relnatts == indnatts` check at
   `postgres/src/backend/utils/cache/relcache.c:1492` passes.

3. **`internal/initdb/initdb.go` global/ placeholder list** (the `for _,
   oid := range []uint32{ 2671, 2672, … 3593, … }` block under
   `bootstrapPostgresDatabase`) — add `6246, //
   pg_parameter_acl_parname_index (Step 3bq)` so an empty PG-conformant
   btree metapage is written to `global/6246` before any backend tries
   to `mdopen` the file. Step-3k's `makeBtreeRootPage` already produces a
   PG18-compliant empty-btree metapage (`btm_root = P_NONE`), which is
   correct here because `pg_parameter_acl` itself is empty (no GRANTed
   parameters are bootstrapped).

The seed threads automatically through the existing flow:
`bootstrapPgClassTuples` writes the `Form_pg_class` row for OID 6246,
`bootstrapPgAttributeTuples` writes 1 pg_attribute row (the parname key
column), `bootstrapPgIndexTuples` writes the `Form_pg_index` row and
captures its heap TID in `pgIndexTIDs`, then
`bootstrapPgIndexIndexrelidIndex` adds the leaf to the populated
2-page btree at `base/{1,5}/2679 + global/2679`, and
`bootstrapPgClassOidIndex` adds the leaf at file 2662.

## Why text_ops, not name_ops

`parname` is `text`, not `name`. PG18's `pg_parameter_acl.h` declares
`parname text` (line 36), and the index uses `text_ops`. This differs
from most catalog name-keyed indexes (e.g. `pg_database_datname_index`,
`pg_namespace_nspname_index`) which use `name_ops`. The text_ops slot
still carries `C_COLLATION_OID = 950` per the existing convention used
by `pg_shseclabel_object_index`'s `provider text_ops` key.

## Verification

```
go build ./...                                          # PASS
go test -count=1 -run \
  'TestPgParameterAclParnameIndex|TestNailedSharedRelsContainsPgParameterAclParnameIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestNailedRelTypesMatchPG18FormrdescConstants|TestNailedSharedRelsContainsPgParameterAcl|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages' \
  ./internal/initdb/                                    # PASS
go test -count=1 ./internal/initdb/                     # 14 pre-existing baseline failures unchanged
go test -count=1 ./internal/executor/ ./internal/server/ \
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/  # PASS
```

The 14 baseline initdb failures (`TestMigration*`, `TestCreate*`,
`TestBootstrappedPG{Class,Attribute}RowsReadable`,
`TestSynchronousCommitFlushesByDefault`,
`TestOpenOldClusterWithoutM0030FilesStillWorks`,
`TestSystemCatalogRelfilesAreValidHeapPages`,
`TestCommittedTableSurvivesCrashRestart`,
`TestRuntimeCloseTriggersFinalCheckpoint`,
`TestMultipleTablesLoadFromHeap`) match the Step 3bp baseline exactly —
no new regressions introduced.

## Regression pins

- `TestPgParameterAclParnameIndexSeededFromInitialEntries` — pins
  `(IndRelid=6243, IndKey=[2], IsUnique=true, IsPrimary=false,
  IndClass=[3126], IndCollation=[950])` against the
  `pgIndexInitialEntries` row for OID 6246.
- `TestNailedSharedRelsContainsPgParameterAclParnameIndex` — pins
  `(RelName="pg_parameter_acl_parname_index", RelKind='i', RelNatts=1)`
  against the `nailedSharedRels` entry for OID 6246.
- `TestPgIndexInitialEntriesIndkeyMatchesPG18` — pinned-map count now 56
  → 57; rejects future additions that forget to update the map.
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave` —
  extended with 6246 so the populated 2-page btree at file 2679 must
  carry this leaf (otherwise `SearchSysCache1(INDEXRELID, 6246)` would
  miss and re-introduce the FATAL).

## Next blocker (Step 3br)

Per the pattern: re-run `GOOPG_RUN_BLOCKED_M0102_E2E=1
TestE2E_FailoverGoopgToPG/async`, expect FATAL `could not open relation
with OID 6247` = `pg_parameter_acl_oid_index` (UNIQUE PRIMARY KEY on
oid). Same seed pattern as the OID-PKEY companion of every other
catalog (e.g. Step 3bo, Step 3bk, Step 3bg). Single oid_ops key, no
collation; pinned in pgIndexInitialEntries shared slice and
nailedSharedRels.
