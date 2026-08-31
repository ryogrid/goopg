# M0106-0010 Step 3aj — pg_conversion_name_nsp_index (OID 2669)

Status: landed (2026-05-18).

## Context

Step 3ai closed the PG-standby boot blocker for OID 2670
(`pg_conversion_oid_index`), the UNIQUE PRIMARY backing the `CONOID`
syscache. The last remaining pg_conversion companion index per
`postgres/src/include/catalog/pg_conversion.h:63-65` is OID 2669
(`pg_conversion_name_nsp_index`), the UNIQUE (non-PKEY) composite
backing the `CONNAMENSP` syscache lookup. Step 3aj seeds it, completing
the pg_conversion index family.

The pattern follows previous composite UNIQUE non-PKEY seeds — most
directly `pg_collation_name_enc_nsp_index` (3164, Step 3ae) and
`pg_opclass_am_name_nsp_index` (2686, Step 3ad), both of which lead
with a `name`-typed `name_ops` key carrying `C_COLLATION_OID = 950`.

## Authoritative source

`postgres/src/include/catalog/pg_conversion.h:64`:

```
DECLARE_UNIQUE_INDEX(pg_conversion_name_nsp_index, 2669,
    ConversionNameNspIndexId, pg_conversion,
    btree(conname name_ops, connamespace oid_ops));
MAKE_SYSCACHE(CONNAMENSP, pg_conversion_name_nsp_index, 8);
```

`pg_conversion_d.h`: `conname` is attnum 2, `connamespace` is attnum 3.

## Change

Pure catalog-seed addition — no encoder, builder, or `Init` flow change.

### `internal/initdb/initdb.go::pgIndexInitialEntries`

```go
entry(2669, 2607, []int16{2, 3}, []uint32{nameOps, oidOps}, []uint32{cCollation, 0}, true, false)
//    OID  rel    keys             opclass                   collation             unique primary
```

`IsUnique = true`, `IsPrimary = false` — `DECLARE_UNIQUE_INDEX` without
the `_PKEY` variant. `IndCollation[0] = cCollation (950)` because the
`name_ops` btree opclass uses C collation. `IndCollation[1] = 0`
because `oid_ops` carries no collation.

### `internal/initdb/initdb.go::bootstrapPostgresDatabase`

The three placeholder OID lists (`base/1/`, `base/5/`, `global/`) gain
`2669, // pg_conversion_name_nsp_index (Step 3aj)` between 2668 and
2670. The Step-3k empty-btree placeholder (`btm_root = P_NONE`) is
sufficient because `pg_conversion` is currently unpopulated, so a
zero-row `CONNAMENSP` lookup is the expected outcome at this stage.

### `internal/initdb/relcache_init.go::nailedLocalRels`

`{2669, "pg_conversion_name_nsp_index"}` is added immediately after the
Step 3ai entry. `flattenRels` derives `RelKind = 'i'` and
`RelNatts = 2` via `pgIndexNattsByOID(2669) → 2`, satisfying
`RelationInitIndexAccessInfo`'s `relnatts == indnatts` consistency
check (`relcache.c:1492`).

## Flow

The seed threads automatically through the existing bootstrap:

1. `bootstrapPgClassTuples` writes `Form_pg_class` row for OID 2669.
2. `bootstrapPgAttributeTuples` writes 2 `indexKeyAttrs` rows.
3. `bootstrapPgIndexTuples` writes the `Form_pg_index` row with
   `indnatts = 2`, captures the heap TID in `pgIndexTIDs[2669]`.
4. `bootstrapPgIndexIndexrelidIndex` adds the OID-2669 leaf to the
   populated 2-page btree at file 2679.
5. `bootstrapPgClassOidIndex` adds the OID-2669 leaf to file 2662.
6. `bootstrapPgAttributeRelidAttnumIndex` adds the composite-key
   leaves to file 2659.
7. `writeRelcacheInitFile` emits the `Form_pg_class` + 2
   `Form_pg_attribute` blob group.

## Regression pins

* `TestPgConversionNameNspIndexSeededFromInitialEntries` —
  `(IndRelid=2607, IndKey=[2 3], IsUnique=true, IsPrimary=false,
  IndCollation=[950, 0])`.
* `TestNailedLocalRelsContainsPgConversionNameNspIndex` —
  `RelName="pg_conversion_name_nsp_index", RelKind='i', RelNatts=2`.

Both in `internal/initdb/pg_conversion_name_nsp_index_test.go`.

Existing pins extended in lockstep:

* `TestPgIndexInitialEntriesIndkeyMatchesPG18`: adds `2669: {2, 3}` to
  the authoritative map. The strict count guard auto-rejects future
  additions without updating this map.
* `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`:
  the populated `pg_index_indexrelid_index` (file 2679) btree must
  carry an OID-2669 leaf.

## Verification

```
go build ./...                                       — PASS
go test -count=1 -run \
  'TestPgConversionNameNspIndex|TestNailedLocalRelsContainsPgConversionNameNspIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestPgConversionDefaultIndex|TestPgConversionOidIndex|TestNailedLocalRelsContainsPgConversion' \
  ./internal/initdb/                                  — PASS
go test -count=1 ./internal/initdb/                  — same baseline failures as Step 3ai (no new regressions)
go test -count=1 ./internal/executor/ \
                 ./internal/server/ \
                 ./internal/storage/ \
                 ./internal/catalog/ \
                 ./internal/mvcc/                    — PASS
```

## Next blocker

With Steps 3ah/3ai/3aj all landed, the pg_conversion index family is
complete (2668/2669/2670). The next anticipated E2E re-run blocker will
likely be a different `pg_*_index` OID surfaced by
`RelationCacheInitializePhase3`'s nailed-index walk. The same
single-OID catalog-seed-addition pattern applies to whichever index
surfaces next.

## Files

* `internal/initdb/initdb.go`
* `internal/initdb/relcache_init.go`
* `internal/initdb/pg_index_indkey_test.go`
* `internal/initdb/btree_index_bootstrap_test.go`
* `internal/initdb/pg_conversion_name_nsp_index_test.go` (new)
* `docs/design/0106-0010-step3aj-pg-conversion-name-nsp-index.md` (this doc)
