# M0106-0010 Step 3ap — pg_enum_typid_label_index (OID 3503)

## Context

Step 3ao seeded `pg_enum_oid_index` (OID 3502), the UNIQUE PRIMARY key on
`pg_enum.oid`. The next catalog object surfaced by PG's
`RelationCacheInitializePhase3` walk is the UNIQUE non-PKEY composite index
on `(enumtypid, enumlabel)` — OID 3503, declared in
`postgres/src/include/catalog/pg_enum.h:48`. Without it,
`RelationIdGetRelation(3503)` FATALs with `could not open relation with OID
3503` because no `pg_class` row is seeded.

## Authoritative source

`postgres/src/include/catalog/pg_enum.h:48`:

```c
DECLARE_UNIQUE_INDEX(pg_enum_typid_label_index, 3503,
    EnumTypIdLabelIndexId, pg_enum,
    btree(enumtypid oid_ops, enumlabel name_ops));
```

`pg_enum_d.h`: `#define EnumTypIdLabelIndexId 3503`. Backs
`MAKE_SYSCACHE(ENUMTYPOIDNAME, pg_enum_typid_label_index, 8)`.

`pg_enum.h` attnums (`pg_enum_d.h`):
- 1 = `oid`
- 2 = `enumtypid` (Oid)
- 3 = `enumsortorder` (float4)
- 4 = `enumlabel` (NameData)

Composite shape `(oid_ops, name_ops)` matches:
- 2669 (`pg_conversion_name_nsp_index`, Step 3aj) — `(conname name_ops,
  connamespace oid_ops)` — same opclass pair with columns swapped
- 2686 (`pg_opclass_am_name_nsp_index`, Step 3ad) — `(opcmethod oid_ops,
  opcname name_ops, opcnamespace oid_ops)` — same leading-oid /
  middle-name pattern

The `name_ops` slot carries C collation (`C_COLLATION_OID = 950`) in
`indcollation`, identical convention to every previous nailed `name_ops`
key column.

## Decision

Pure catalog-seed addition; no encoder, builder, or `Init` flow change.
Continues the single-OID rhythm established since Step 3w.

### Changes

(a) `internal/initdb/initdb.go::pgIndexInitialEntries` gains
`entry(3503, 3501, []int16{2, 4}, []uint32{oidOps, nameOps},
[]uint32{0, cCollation}, true, false)` — UNIQUE non-PKEY composite over
the pg_enum heap (3501, Step 3an). `enumtypid` (attno 2) uses `oid_ops`
with no collation; `enumlabel` (attno 4) uses `name_ops` with
`C_COLLATION_OID = 950`.

(b) `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec gains
`{3503, "pg_enum_typid_label_index"}`. `flattenRels` derives
`RelKind='i', RelNatts=2` via `pgIndexNattsByOID` so
`RelationInitIndexAccessInfo`'s `relnatts == indnatts` check at
`relcache.c:1492` passes.

(c) Three placeholder OID lists in `bootstrapPostgresDatabase`
(`base/1/`, `base/5/`, `global/`) gain
`3503, // pg_enum_typid_label_index (Step 3ap)`. The Step-3k empty btree
placeholder is sufficient because `pg_enum` has zero rows at initdb time
(any ENUMTYPOIDNAME syscache lookup at early boot expects NULL).

### Automatic threading

The seed flows through the existing pipeline without code changes:

```
bootstrapPgClassTuples            → Form_pg_class row for 3503
bootstrapPgAttributeTuples        → 2 indexKeyAttrs rows (enumtypid, enumlabel)
bootstrapPgIndexTuples            → Form_pg_index row with indnatts=2;
                                    TID captured in pgIndexTIDs[3503]
bootstrapPgIndexIndexrelidIndex   → leaf at file 2679 (per-DB + global)
bootstrapPgClassOidIndex          → leaf at 2662
bootstrapPgAttributeRelidAttnumIndex → 2 composite-key leaves at 2659
```

## Tests

New file `internal/initdb/pg_enum_typid_label_index_test.go`:
- `TestPgEnumTypIdLabelIndexSeededFromInitialEntries` — asserts
  `(IndRelid=3501, IndKey=[2 4], IsUnique=true, IsPrimary=false,
   IndCollation=[0 950])`.
- `TestNailedLocalRelsContainsPgEnumTypIdLabelIndex` — asserts
  `RelName="pg_enum_typid_label_index", RelKind='i', RelNatts=2`.

Existing pins extended:
- `TestPgIndexInitialEntriesIndkeyMatchesPG18` map gains `3503: {2, 4}`
  (strict count guard auto-rejects future additions without map updates).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  gains 3503 so the populated 2679 btree must carry this leaf.

## Verification

- `go build ./...` PASS.
- `go test -count=1 -run
  'TestPgEnumTypIdLabelIndex|TestNailedLocalRelsContainsPgEnumTypIdLabelIndex|TestPgEnumOidIndex|TestNailedLocalRelsContainsPgEnumOidIndex|TestNailedLocalRelsContainsPgEnum|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestNailedIndexRelnattsAgreesWithIndnatts|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages'
  ./internal/initdb/` PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3ao; no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/`
  PASS.

## Next blocker

With OID 3503 opened cleanly, the next E2E re-run is expected to surface
OID 3534 (`pg_enum_typid_sortorder_index`, UNIQUE composite
btree(enumtypid oid_ops, enumsortorder float4_ops)) — the first nailed
index to key on `float4_ops` opclass, which requires an opclass-inventory
check (Step 3aq). Alternatively, another single-OID nailed-rel/index OID
may surface from `RelationCacheInitializePhase3`'s nailed-rel walk. Same
single-OID catalog-seed-addition pattern applies.
