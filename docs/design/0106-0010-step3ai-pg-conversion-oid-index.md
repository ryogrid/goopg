# M0106-0010 Step 3ai — pg_conversion_oid_index (OID 2670)

Status: landed (2026-05-18).

## Context

Step 3ah closed the PG-standby boot blocker for OID 2668
(`pg_conversion_default_index`). The next anticipated blocker on the
`TestE2E_FailoverGoopgToPG/async` chain is one of the two remaining
pg_conversion companion indexes:

* OID 2669 — `pg_conversion_name_nsp_index` (composite UNIQUE on
  `conname name_ops, connamespace oid_ops`)
* OID 2670 — `pg_conversion_oid_index` (single-column UNIQUE PRIMARY on
  `oid oid_ops`)

Step 3ai seeds OID 2670 (the PKEY backing the `CONOID` syscache
lookup), which mirrors the single-column oid PKEY pattern already used
for `pg_cast_oid_index` (Step 3ab), `pg_collation_oid_index` (Step 3af),
and `pg_opclass_oid_index` (Step 3l). Step 3aj will close OID 2669
when the next E2E re-run surfaces it.

## Authoritative source

`postgres/src/include/catalog/pg_conversion.h:65`:

```
DECLARE_UNIQUE_INDEX_PKEY(pg_conversion_oid_index, 2670,
    ConversionOidIndexId, pg_conversion, btree(oid oid_ops));
```

`pg_conversion_d.h`: oid is attnum 1.

## Change

Pure catalog-seed addition — no encoder, builder, or `Init` flow change.

### `internal/initdb/initdb.go::pgIndexInitialEntries`

```go
entry(2670, 2607, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)
//    OID  rel   keys         opclass        coll  unique primary
```

`IsPrimary = true` and `IsUnique = true` because the declaration is
`DECLARE_UNIQUE_INDEX_PKEY`. `IndCollation[0] = 0` because `oid_ops`
carries no collation.

### `internal/initdb/initdb.go::bootstrapPostgresDatabase`

The three placeholder OID lists (`base/1/`, `base/5/`, `global/`) at
lines 685–687, 776–778, 802–804 gain `2670, // pg_conversion_oid_index
(Step 3ai)`. The Step-3k empty-btree placeholder (`btm_root = P_NONE`)
is sufficient because `pg_conversion` is currently unpopulated, so a
zero-row `CONOID` lookup is the expected outcome at this stage.

### `internal/initdb/relcache_init.go::nailedLocalRels`

`{2670, "pg_conversion_oid_index"}` is added immediately after the
Step 3ah entry. `flattenRels` derives `RelKind = 'i'` and
`RelNatts = 1` via `pgIndexNattsByOID(2670) → 1`, satisfying
`RelationInitIndexAccessInfo`'s `relnatts == indnatts` consistency
check (`relcache.c:1492`).

## Flow

The seed threads automatically through the existing bootstrap:

1. `bootstrapPgClassTuples` writes `Form_pg_class` row for OID 2670.
2. `bootstrapPgAttributeTuples` writes 1 `indexKeyAttrs` row.
3. `bootstrapPgIndexTuples` writes the `Form_pg_index` row with
   `indnatts = 1`, captures the heap TID in `pgIndexTIDs[2670]`.
4. `bootstrapPgIndexIndexrelidIndex` adds the OID-2670 leaf to the
   populated 2-page btree at file 2679.
5. `bootstrapPgClassOidIndex` adds the OID-2670 leaf to file 2662.
6. `bootstrapPgAttributeRelidAttnumIndex` adds the composite-key leaf
   to file 2659.
7. `writeRelcacheInitFile` emits the `Form_pg_class` + 1
   `Form_pg_attribute` blob group.

## Regression pins

* `TestPgConversionOidIndexSeededFromInitialEntries` —
  `(IndRelid=2607, IndKey=[1], IsUnique=true, IsPrimary=true,
  IndCollation=[0])`.
* `TestNailedLocalRelsContainsPgConversionOidIndex` —
  `RelName="pg_conversion_oid_index", RelKind='i', RelNatts=1`.

Both in `internal/initdb/pg_conversion_oid_index_test.go`.

Existing pins extended in lockstep:

* `TestPgIndexInitialEntriesIndkeyMatchesPG18`: adds `2670: {1}` to the
  authoritative map. The strict count guard auto-rejects future
  additions without updating this map.
* `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`:
  the populated `pg_index_indexrelid_index` (file 2679) btree must
  carry an OID-2670 leaf.

## Verification

```
go build ./...                                       — PASS
go test -count=1 -run \
  'TestPgConversionOidIndex|TestNailedLocalRelsContainsPgConversionOidIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestPgConversionDefaultIndex|TestNailedLocalRelsContainsPgConversion' \
  ./internal/initdb/                                  — PASS
go test -count=1 ./internal/initdb/                  — 14 pre-existing baseline failures unchanged from Step 3ah; no new regressions
go test -count=1 ./internal/executor/ \
                 ./internal/server/ \
                 ./internal/storage/ \
                 ./internal/catalog/ \
                 ./internal/mvcc/                    — PASS
```

## Next blocker

After Step 3ai, the next E2E re-run is expected to surface OID 2669
(`pg_conversion_name_nsp_index`, 2-column UNIQUE on
`(conname name_ops, connamespace oid_ops)`) — the last remaining
pg_conversion companion index per `pg_conversion.h:64`. Step 3aj will
close it with the same single-OID catalog-seed-addition pattern.

## Files

* `internal/initdb/initdb.go`
* `internal/initdb/relcache_init.go`
* `internal/initdb/pg_index_indkey_test.go`
* `internal/initdb/btree_index_bootstrap_test.go`
* `internal/initdb/pg_conversion_oid_index_test.go` (new)
* `docs/design/0106-0010-step3ai-pg-conversion-oid-index.md` (this doc)
