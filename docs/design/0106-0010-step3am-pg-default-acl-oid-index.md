# M0106-0010 Step 3am — pg_default_acl_oid_index (OID 828)

## Context

Step 3ak seeded `pg_default_acl` (OID 826) as a nailed local relation and Step
3al seeded its composite-UNIQUE companion index 827
(`pg_default_acl_role_nsp_obj_index`). The remaining catalog object in the
`pg_default_acl` family is its UNIQUE PRIMARY KEY index OID 828
(`pg_default_acl_oid_index`). Without it, after `RelationCacheInitializePhase3`
clears all earlier blockers, the next forked backend's
`RelationIdGetRelation(828)` FATALs with `could not open relation with OID 828`
because no `pg_class` row is seeded.

## Authoritative source

`postgres/src/include/catalog/pg_default_acl.h:55`:

```c
DECLARE_UNIQUE_INDEX_PKEY(pg_default_acl_oid_index, 828,
    DefaultAclOidIndexId, pg_default_acl, btree(oid oid_ops));
```

`pg_default_acl_d.h:25`: `#define DefaultAclOidIndexId 828`.

Single-column oid PKEY pattern, identical to:
- 2660 (`pg_cast_oid_index`, Step 3ab)
- 2670 (`pg_conversion_oid_index`, Step 3ai)
- 2687 (`pg_opclass_oid_index`, Step 3l)
- 3085 (`pg_collation_oid_index`, Step 3af)

## Decision

Pure catalog-seed addition; no encoder, builder, or `Init` flow change. Mirrors
the single-OID rhythm of Steps 3w → 3x → 3y → ….

### Changes

(a) `internal/initdb/initdb.go::pgIndexInitialEntries` gains
`entry(828, 826, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)` —
UNIQUE PRIMARY (single oid_ops key, no collation).

(b) `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec gains
`{828, "pg_default_acl_oid_index"}`. `flattenRels` derives
`RelKind='i', RelNatts=1` via `pgIndexNattsByOID` so
`RelationInitIndexAccessInfo`'s `relnatts == indnatts` check at
`relcache.c:1492` passes.

(c) Three placeholder OID lists in `bootstrapPostgresDatabase`
(`base/1/`, `base/5/`, `global/`) gain
`828, // pg_default_acl_oid_index (Step 3am)`. The Step-3k empty btree
placeholder (`btm_root = P_NONE`) is sufficient because `pg_default_acl` is
currently unpopulated (a zero-row OID lookup is the expected outcome at this
stage).

### Automatic threading

The seed flows through the existing pipeline without code changes:

```
bootstrapPgClassTuples            → Form_pg_class row for 828
bootstrapPgAttributeTuples        → 1 indexKeyAttrs row (oid column)
bootstrapPgIndexTuples            → Form_pg_index row; TID captured in pgIndexTIDs[828]
bootstrapPgIndexIndexrelidIndex   → leaf at file 2679
bootstrapPgClassOidIndex          → leaf at 2662
bootstrapPgAttributeRelidAttnumIndex → composite-key leaf at 2659
```

## Tests

New file `internal/initdb/pg_default_acl_oid_index_test.go`:
- `TestPgDefaultAclOidIndexSeededFromInitialEntries` — asserts
  `(IndRelid=826, IndKey=[1], IsUnique=true, IsPrimary=true,
   IndCollation=[0])`.
- `TestNailedLocalRelsContainsPgDefaultAclOidIndex` — asserts
  `RelName="pg_default_acl_oid_index", RelKind='i', RelNatts=1`.

Existing pins extended:
- `TestPgIndexInitialEntriesIndkeyMatchesPG18` map gains `828: {1}` (strict
  count guard auto-rejects future additions without map updates).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave` gains
  828 so the populated 2679 btree must carry this leaf.

## Verification

- `go build ./...` PASS.
- `go test -count=1 -run
  'TestPgDefaultAclOidIndex|TestNailedLocalRelsContainsPgDefaultAclOidIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestPgDefaultAclRoleNspObjIndex|TestNailedLocalRelsContainsPgDefaultAcl|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3al (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/`
  — PASS.

## Next blocker

With OID 828 opened cleanly, the pg_default_acl family is fully seeded. The
next E2E re-run will surface either another single-OID nailed-rel/index
blocker (same catalog-seed-addition pattern applies) or a deeper issue
requiring populated btree content. Step 3an territory.
