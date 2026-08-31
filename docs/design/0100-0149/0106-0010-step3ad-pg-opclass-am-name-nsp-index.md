# M0106-0010 Step 3ad — pg_opclass_am_name_nsp_index (OID 2686)

## Context

After Step 3ac landed the catalog seed for `pg_cast_source_target_index`
(OID 2661), the next `GOOPG_RUN_BLOCKED_M0102_E2E=1
TestE2E_FailoverGoopgToPG/async` re-run produced a fresh blocker:

```
FATAL: could not open relation with OID 2686
```

repeated on every backend the standby's postmaster forked.

## Authoritative source

`postgres/src/include/catalog/pg_opclass.h:85`:

```c
DECLARE_UNIQUE_INDEX(pg_opclass_am_name_nsp_index, 2686,
    OpclassAmNameNspIndexId, pg_opclass,
    btree(opcmethod oid_ops, opcname name_ops, opcnamespace oid_ops));
MAKE_SYSCACHE(CLAAMNAMENSP, pg_opclass_am_name_nsp_index, 8);
```

`pg_opclass_d.h`:

| attnum | name           | type |
| ------ | -------------- | ---- |
| 1      | oid            | oid  |
| 2      | opcmethod      | oid  |
| 3      | opcname        | name |
| 4      | opcnamespace   | oid  |

So OID 2686 is the `CLAAMNAMENSP` syscache backing index on pg_opclass
(OID 2616): three columns, UNIQUE but NOT primary
(`DECLARE_UNIQUE_INDEX`, not the `_PKEY` variant — that's 2687).

## Fix

Pure catalog-seed addition mirroring Step 3ac. No encoder, builder, or
`Init` flow change.

(a) `internal/initdb/initdb.go::pgIndexInitialEntries` gains

```go
entry(2686, 2616, []int16{2, 3, 4},
    []uint32{oidOps, nameOps, oidOps},
    []uint32{0, cCollation, 0},
    true, false), // pg_opclass_am_name_nsp_index
```

— `opcname` is a `name` column whose btree opclass uses C collation
(`C_COLLATION_OID = 950`), same as `pg_database_datname_index` (2671)
and `pg_namespace_nspname_index` (2684).

(b) `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec gains
`{2686, "pg_opclass_am_name_nsp_index"}`. `flattenRels` consults
`pgIndexNattsByOID()` (returns 3 for OID 2686) so the nailed rel
carries `RelKind='i', RelNatts=3` and PG's
`RelationInitIndexAccessInfo` `relnatts == indnatts` check passes.

(c) Three placeholder OID lists in `bootstrapPostgresDatabase`
(`base/1/`, `base/5/`, `global/`) gain `2686, //
pg_opclass_am_name_nsp_index (Step 3ad)`. The Step-3k empty btree
placeholder (`btm_root = P_NONE`) is sufficient: the `CLAAMNAMENSP`
syscache is satisfied by pg_opclass's already-populated
`pg_opclass_oid_index` (2687, populated in Step 3l) — opening 2686's
relcache entry alone clears the FATAL.

## Threading

The seed reuses the existing bootstrap flow without modification:

1. `bootstrapPgClassTuples` writes a `Form_pg_class` row for 2686.
2. `bootstrapPgAttributeTuples` writes 3 `indexKeyAttrs` rows.
3. `bootstrapPgIndexTuples` writes a 21-column `Form_pg_index` heap
   row with `indnatts = 3` and captures its TID into `pgIndexTIDs[2686]`.
4. `bootstrapPgIndexIndexrelidIndex` adds a leaf to the populated
   2-page btree at file 2679.
5. `bootstrapPgClassOidIndex` adds a leaf at file 2662.
6. `bootstrapPgAttributeRelidAttnumIndex` adds 3 composite-key leaves
   at file 2659.

## Test pins

New (`internal/initdb/pg_opclass_am_name_nsp_index_test.go`):

- `TestPgOpclassAmNameNspIndexSeededFromInitialEntries` asserts
  `(IndRelid=2616, IndKey=[2,3,4], IsUnique=true, IsPrimary=false,
  IndCollation=[0,950,0])`.
- `TestNailedLocalRelsContainsPgOpclassAmNameNspIndex` asserts
  `(RelName="pg_opclass_am_name_nsp_index", RelKind='i', RelNatts=3)`.

Extended pins:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` adds `2686: {2,3,4}`
  (strict count guard auto-rejects future additions without map
  updates).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with 2686 so the populated 2679 btree must carry the leaf.

## Verification

```
go build ./...
go test -count=1 -run \
  'TestPgOpclassAmNameNspIndex|TestNailedLocalRelsContainsPgOpclassAmNameNspIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestPgCastSourceTargetIndex|TestPgCastOidIndex|TestPgAggregateFnoidIndex|TestPgAmopFamStrat' \
  ./internal/initdb/
go test -count=1 ./internal/initdb/
go test -count=1 ./internal/executor/ ./internal/server/ \
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/
```

## Next blocker

Future Step 3ae will surface on the next E2E re-run — typical
candidates are pg_constraint sibling indexes, pg_conversion family,
or pg_attrdef family, each of which is a pure-seed catalog addition
following the same pattern as Steps 3w → 3aa → 3ad.
