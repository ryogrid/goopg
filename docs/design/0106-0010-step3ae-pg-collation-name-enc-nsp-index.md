# M0106-0010 Step 3ae — pg_collation_name_enc_nsp_index (OID 3164)

## Context

After Step 3ad landed the catalog seed for `pg_opclass_am_name_nsp_index`
(OID 2686), the next `GOOPG_RUN_BLOCKED_M0102_E2E=1
TestE2E_FailoverGoopgToPG/async` re-run produced a fresh blocker:

```
FATAL: could not open relation with OID 3164
```

repeated on every backend the standby's postmaster forked.

## Authoritative source

`postgres/src/include/catalog/pg_collation.h:62`:

```c
DECLARE_UNIQUE_INDEX(pg_collation_name_enc_nsp_index, 3164,
    CollationNameEncNspIndexId, pg_collation,
    btree(collname name_ops, collencoding int4_ops, collnamespace oid_ops));
MAKE_SYSCACHE(COLLNAMEENCNSP, pg_collation_name_enc_nsp_index, 8);
```

`pg_collation_d.h`:

| attnum | name                  | type |
| ------ | --------------------- | ---- |
| 1      | oid                   | oid  |
| 2      | collname              | name |
| 3      | collnamespace         | oid  |
| 4      | collowner             | oid  |
| 5      | collprovider          | char |
| 6      | collisdeterministic   | bool |
| 7      | collencoding          | int4 |
| 8      | collcollate           | text |
| 9      | collctype             | text |

So OID 3164 is the `COLLNAMEENCNSP` syscache backing index on pg_collation
(OID 3456): three columns, UNIQUE but NOT primary
(`DECLARE_UNIQUE_INDEX`, not the `_PKEY` variant — that's 3085 =
`pg_collation_oid_index`).

## Fix

Pure catalog-seed addition mirroring Step 3ad. No encoder, builder, or
`Init` flow change.

(a) `internal/initdb/initdb.go::pgIndexInitialEntries` gains

```go
entry(3164, 3456, []int16{2, 7, 3},
    []uint32{nameOps, int4Ops, oidOps},
    []uint32{cCollation, 0, 0},
    true, false), // pg_collation_name_enc_nsp_index
```

— `collname` is a `name` column whose btree opclass uses C collation
(`C_COLLATION_OID = 950`), same as `pg_database_datname_index` (2671),
`pg_namespace_nspname_index` (2684), and `pg_opclass_am_name_nsp_index`
(2686).

Note the attnum order: the PG18 source declares the keys in
`(collname, collencoding, collnamespace)` order — heap attnums 2, 7, 3
respectively. The `collencoding` key uses `int4_ops` (OID 1978), the
first time goopg's index seed list references this opclass.

(b) `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec gains
`{3164, "pg_collation_name_enc_nsp_index"}`. `flattenRels` consults
`pgIndexNattsByOID()` (returns 3 for OID 3164) so the nailed rel
carries `RelKind='i', RelNatts=3` and PG's
`RelationInitIndexAccessInfo` `relnatts == indnatts` check passes.

(c) The three placeholder OID lists in `bootstrapPostgresDatabase`
(`base/1/`, `base/5/`, `global/`) already include `3164` from an earlier
sweep — no edit needed. The Step-3k empty btree placeholder
(`btm_root = P_NONE`) is sufficient: the `COLLNAMEENCNSP` syscache is
satisfied by pg_collation's already-placeholder
`pg_collation_oid_index` (3085) entries; opening 3164's relcache entry
alone clears the FATAL because pg_collation is currently unpopulated
(no collation rows are bootstrapped), so a zero-row 3-column-composite
lookup is the expected outcome.

## Threading

The seed reuses the existing bootstrap flow without modification:

1. `bootstrapPgClassTuples` writes a `Form_pg_class` row for 3164.
2. `bootstrapPgAttributeTuples` writes 3 `indexKeyAttrs` rows.
3. `bootstrapPgIndexTuples` writes a 21-column `Form_pg_index` heap
   row with `indnatts = 3` and captures its TID into `pgIndexTIDs[3164]`.
4. `bootstrapPgIndexIndexrelidIndex` adds a leaf to the populated
   2-page btree at file 2679.
5. `bootstrapPgClassOidIndex` adds a leaf at file 2662.
6. `bootstrapPgAttributeRelidAttnumIndex` adds 3 composite-key leaves
   at file 2659.

## Test pins

New (`internal/initdb/pg_collation_name_enc_nsp_index_test.go`):

- `TestPgCollationNameEncNspIndexSeededFromInitialEntries` asserts
  `(IndRelid=3456, IndKey=[2,7,3], IsUnique=true, IsPrimary=false,
  IndCollation=[950,0,0])`.
- `TestNailedLocalRelsContainsPgCollationNameEncNspIndex` asserts
  `(RelName="pg_collation_name_enc_nsp_index", RelKind='i', RelNatts=3)`.

Extended pins:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` adds `3164: {2,7,3}`
  (strict count guard auto-rejects future additions without map
  updates).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with 3164 so the populated 2679 btree must carry the leaf.

## Verification

```
go build ./...
go test -count=1 -run \
  'TestPgCollationNameEncNspIndex|TestNailedLocalRelsContainsPgCollationNameEncNspIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestPgOpclassAmNameNspIndex|TestPgCastSourceTargetIndex|TestPgCastOidIndex|TestPgAggregateFnoidIndex|TestPgAmopFamStrat' \
  ./internal/initdb/
go test -count=1 ./internal/initdb/
go test -count=1 ./internal/executor/ ./internal/server/ \
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/
```

## Next blocker

Future Step 3af will surface on the next E2E re-run — typical
candidates are pg_collation's companion oid index (3085), pg_constraint
sibling indexes (2664/2665/2666), pg_conversion family (2668/2669/2670),
or pg_attrdef family (2656/2657), each of which is a pure-seed catalog
addition following the same pattern as Steps 3w → 3aa → 3ad → 3ae.
