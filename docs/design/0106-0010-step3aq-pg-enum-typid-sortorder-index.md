# M0106-0010 Step 3aq — pg_enum_typid_sortorder_index (OID 3534)

## Context

Step 3ap seeded `pg_enum_typid_label_index` (OID 3503), the UNIQUE non-PKEY
`(enumtypid oid_ops, enumlabel name_ops)` composite over `pg_enum`. The next
catalog object surfaced by PG's `RelationCacheInitializePhase3` walk is the
sibling UNIQUE non-PKEY composite `(enumtypid oid_ops, enumsortorder float4_ops)`
— OID 3534, declared in `postgres/src/include/catalog/pg_enum.h:48`. Without it,
`RelationIdGetRelation(3534)` FATALs with `could not open relation with OID 3534`
because no `pg_class` row is seeded.

Step 3aq is also the first nailed index keyed on the `float4_ops` btree opclass,
which requires adding a new opclass-OID constant to the index-entries builder.

## Authoritative source

`postgres/src/include/catalog/pg_enum.h:48`:

```c
DECLARE_UNIQUE_INDEX(pg_enum_typid_sortorder_index, 3534,
    EnumTypIdSortOrderIndexId, pg_enum,
    btree(enumtypid oid_ops, enumsortorder float4_ops));
```

`pg_enum_d.h`: `#define EnumTypIdSortOrderIndexId 3534`. Unlike its siblings
3502 (ENUMOID) and 3503 (ENUMTYPOIDNAME), 3534 is not directly backed by a
syscache — it exists to support PG's `EnumValuesCreate`/`AddEnumLabel`
ordering scans.

`pg_enum.h` attnums (`pg_enum_d.h`):
- 1 = `oid`
- 2 = `enumtypid` (Oid)
- 3 = `enumsortorder` (float4)
- 4 = `enumlabel` (NameData)

## Opclass OID for float4_ops

`pg_opclass.dat` declares `float4_ops` (btree) without an explicit
`oid_symbol`, so PG auto-assigns one. Authoritative resolution comes from the
generated bki:

```text
postgres/src/backend/catalog/postgres.bki
  insert ( 10012 403 float4_ops 11 10 1970 700 t 0 )
```

Decoding: `10012` = opclass OID; `403` = am OID (btree); `1970` = opfamily OID
(`btree/float_ops`); `700` = intype (float4). The companion line `10013` is the
hash opclass for float4 — not used here. We add a new constant
`float4Ops uint32 = 10012` alongside the existing `oidOps`, `int2Ops`, etc.

Composite shape `(oid_ops, float4_ops)` is structurally identical to
`(oid_ops, int2_ops)` (e.g. `pg_attribute_relid_attnum_index`, 2659) — both
are leading-oid + scalar-numeric pairings where neither key carries a
collation. `float4_ops` has no collation slot because float4 is a scalar
numeric type.

## Decision

Pure catalog-seed addition; no encoder, builder, or `Init` flow change.
Continues the single-OID rhythm established since Step 3w.

### Changes

(a) `internal/initdb/initdb.go::pgIndexInitialEntries` adds a new opclass-OID
constant `float4Ops uint32 = 10012` and a new entry
`entry(3534, 3501, []int16{2, 3}, []uint32{oidOps, float4Ops},
[]uint32{0, 0}, true, false)` — UNIQUE non-PKEY composite over the pg_enum
heap (3501, Step 3an). `enumtypid` (attno 2) uses `oid_ops` with no
collation; `enumsortorder` (attno 3) uses `float4_ops` with no collation.

(b) `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec gains
`{3534, "pg_enum_typid_sortorder_index"}`. `flattenRels` derives
`RelKind='i', RelNatts=2` via `pgIndexNattsByOID` so
`RelationInitIndexAccessInfo`'s `relnatts == indnatts` check at
`relcache.c:1492` passes. The Step 3ao comment block on entry 3502 is updated
in-place to note that companion 3534 is now seeded (no longer deferred).

(c) Three placeholder OID lists in `bootstrapPostgresDatabase`
(`base/1/`, `base/5/`, `global/`) gain
`3534, // pg_enum_typid_sortorder_index (Step 3aq)`. The Step-3k empty btree
placeholder is sufficient because `pg_enum` has zero rows at initdb time
(any sortorder-ordered scan at early boot expects no rows).

### Automatic threading

The seed flows through the existing pipeline without code changes:

```
bootstrapPgClassTuples            → Form_pg_class row for 3534
bootstrapPgAttributeTuples        → 2 indexKeyAttrs rows (enumtypid, enumsortorder)
bootstrapPgIndexTuples            → Form_pg_index row with indnatts=2;
                                    TID captured in pgIndexTIDs[3534]
bootstrapPgIndexIndexrelidIndex   → leaf at file 2679 (per-DB + global)
bootstrapPgClassOidIndex          → leaf at 2662
bootstrapPgAttributeRelidAttnumIndex → 2 composite-key leaves at 2659
```

## Tests

New file `internal/initdb/pg_enum_typid_sortorder_index_test.go`:
- `TestPgEnumTypIdSortOrderIndexSeededFromInitialEntries` — asserts
  `(IndRelid=3501, IndKey=[2 3], IsUnique=true, IsPrimary=false,
   IndClass=[1981 10012], IndCollation=[0 0])`. Verifies float4_ops opclass
  OID 10012 explicitly to lock down the postgres.bki cross-reference.
- `TestNailedLocalRelsContainsPgEnumTypIdSortOrderIndex` — asserts
  `RelName="pg_enum_typid_sortorder_index", RelKind='i', RelNatts=2`.

Existing pins extended:
- `TestPgIndexInitialEntriesIndkeyMatchesPG18` map gains `3534: {2, 3}`
  (strict count guard auto-rejects future additions without map updates).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  gains 3534 so the populated 2679 btree must carry this leaf.

## Verification

- `go build ./...` PASS.
- `go test -count=1 -run
  'TestPgEnumTypIdSortOrderIndex|TestNailedLocalRelsContainsPgEnumTypIdSortOrderIndex|TestPgEnumTypIdLabelIndex|TestNailedLocalRelsContainsPgEnumTypIdLabelIndex|TestPgEnumOidIndex|TestNailedLocalRelsContainsPgEnumOidIndex|TestNailedLocalRelsContainsPgEnum|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|TestNailedIndexRelnattsAgreesWithIndnatts|TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages'
  ./internal/initdb/` PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3ap; no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/ ./internal/mvcc/`
  PASS.

## Next blocker

With OID 3534 opened cleanly — exhausting all three `pg_enum.h` indexes — the
next E2E re-run is expected to surface another single-OID nailed-rel or index
OID flagged by `RelationCacheInitializePhase3`'s nailed-rel walk. Likely
candidates are remaining unseeded entries from PG's `pg_class_d.h` /
`pg_index_d.h` catalog inventory not yet covered by Steps 3w–3aq (e.g.
pg_event_trigger, pg_foreign_data_wrapper, pg_foreign_server, pg_inherits,
pg_init_privs, pg_language, pg_largeobject, pg_largeobject_metadata,
pg_partitioned_table, pg_publication*, pg_range, pg_replication_origin,
pg_seclabel, pg_sequence, pg_statistic, pg_subscription*, pg_tablespace,
pg_transform, pg_ts_*). Same single-OID catalog-seed-addition pattern
applies. With this step, all three `float_ops`-family opclasses are
unblocked for future use; the next composite-key first-use is likely to be
`text_pattern_ops` or `int8_ops`.
