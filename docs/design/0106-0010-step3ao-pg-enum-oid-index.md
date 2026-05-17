# M0106-0010 Step 3ao — pg_enum_oid_index (OID 3502)

## Context

Step 3an seeded `pg_enum` (OID 3501) as a nailed local relation. The next
catalog object that PG's `RelationCacheInitializePhase3` walk needs is the
UNIQUE PRIMARY KEY index over `pg_enum.oid` — OID 3502, declared in
`postgres/src/include/catalog/pg_enum.h:47`. Without it,
`RelationIdGetRelation(3502)` FATALs with `could not open relation with OID
3502` because no `pg_class` row is seeded.

## Authoritative source

`postgres/src/include/catalog/pg_enum.h:47`:

```c
DECLARE_UNIQUE_INDEX_PKEY(pg_enum_oid_index, 3502,
    EnumOidIndexId, pg_enum, btree(oid oid_ops));
```

`pg_enum_d.h:30`: `#define EnumOidIndexId 3502`. Backs
`MAKE_SYSCACHE(ENUMOID, pg_enum_oid_index, 8)`.

Single-column oid PKEY pattern, identical to:
- 828   (`pg_default_acl_oid_index`, Step 3am)
- 2660  (`pg_cast_oid_index`, Step 3ab)
- 2670  (`pg_conversion_oid_index`, Step 3ai)
- 2687  (`pg_opclass_oid_index`, Step 3l)
- 3085  (`pg_collation_oid_index`, Step 3af)

## Decision

Pure catalog-seed addition; no encoder, builder, or `Init` flow change.
Mirrors the single-OID rhythm established since Step 3w.

### Changes

(a) `internal/initdb/initdb.go::pgIndexInitialEntries` gains
`entry(3502, 3501, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)` —
UNIQUE PRIMARY (single oid_ops key, no collation, IndRelid=3501).

(b) `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec gains
`{3502, "pg_enum_oid_index"}`. `flattenRels` derives
`RelKind='i', RelNatts=1` via `pgIndexNattsByOID` so
`RelationInitIndexAccessInfo`'s `relnatts == indnatts` check at
`relcache.c:1492` passes.

(c) Three placeholder OID lists in `bootstrapPostgresDatabase`
(`base/1/`, `base/5/`, `global/`) gain
`3502, // pg_enum_oid_index (Step 3ao)`. The Step-3k empty btree placeholder
(`btm_root = P_NONE`) is sufficient because pg_enum has zero rows at
initdb time (any ENUMOID syscache lookup at early boot expects NULL).

### Automatic threading

The seed flows through the existing pipeline without code changes:

```
bootstrapPgClassTuples            → Form_pg_class row for 3502
bootstrapPgAttributeTuples        → 1 indexKeyAttrs row (oid column)
bootstrapPgIndexTuples            → Form_pg_index row; TID captured in pgIndexTIDs[3502]
bootstrapPgIndexIndexrelidIndex   → leaf at file 2679 (per-DB + global)
bootstrapPgClassOidIndex          → leaf at 2662
bootstrapPgAttributeRelidAttnumIndex → composite-key leaf at 2659
```

## Tests

New file `internal/initdb/pg_enum_oid_index_test.go`:
- `TestPgEnumOidIndexSeededFromInitialEntries` — asserts
  `(IndRelid=3501, IndKey=[1], IsUnique=true, IsPrimary=true,
   IndCollation=[0])`.
- `TestNailedLocalRelsContainsPgEnumOidIndex` — asserts
  `RelName="pg_enum_oid_index", RelKind='i', RelNatts=1`.

Existing pins extended:
- `TestPgIndexInitialEntriesIndkeyMatchesPG18` map gains `3502: {1}`
  (strict count guard auto-rejects future additions without map updates).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  gains 3502 so the populated 2679 btree must carry this leaf.

## Verification

- `go build ./...` PASS.
- `go test -count=1 -run
  'TestPgEnumOidIndex|TestNailedLocalRelsContainsPgEnumOidIndex|TestNailedLocalRelsContainsPgEnum|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestNailedIndexRelnattsAgreesWithIndnatts|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages'
  ./internal/initdb/` PASS.
- `go test -count=1 ./internal/initdb/` — same pre-existing baseline
  failures as Step 3an; no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/`
  PASS.

## Next blocker

With OID 3502 opened cleanly, the next E2E re-run will surface one of the
two remaining pg_enum companion indexes:
- 3503 (`pg_enum_typid_label_index`, UNIQUE composite
  btree(enumtypid oid_ops, enumlabel name_ops); backs syscache ENUMTYPOIDNAME)
- 3534 (`pg_enum_typid_sortorder_index`, UNIQUE composite
  btree(enumtypid oid_ops, enumsortorder float4_ops) — first nailed index
  to key on float4_ops opclass; opclass-inventory check needed)

Or another single-OID nailed-rel/index OID flagged by
`RelationCacheInitializePhase3`'s nailed-rel walk. Same single-OID
catalog-seed-addition pattern applies. Step 3ap territory.
