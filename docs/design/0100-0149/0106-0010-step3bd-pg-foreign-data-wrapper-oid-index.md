# M0106-0010 Step 3bd — pg_foreign_data_wrapper_oid_index catalog seed

## Status

LANDED 2026-05-18.

## Problem

After Step 3bc (commit `1e17559`) seeded the
`pg_foreign_data_wrapper_name_index` (OID 548) nailed local rel, the
next PG-standby boot blocker observed under
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` is:

```
FATAL: could not open relation with OID 112
```

OID 112 is the companion `pg_foreign_data_wrapper_oid_index` declared
at `postgres/src/include/catalog/pg_foreign_data_wrapper.h:55`:

```c
DECLARE_UNIQUE_INDEX_PKEY(pg_foreign_data_wrapper_oid_index, 112,
    ForeignDataWrapperOidIndexId, pg_foreign_data_wrapper,
    btree(oid oid_ops));
MAKE_SYSCACHE(FOREIGNDATAWRAPPEROID,
    pg_foreign_data_wrapper_oid_index, 2);
```

This index backs the `FOREIGNDATAWRAPPEROID` syscache — the OID-keyed
lookup used by `get_foreign_data_wrapper`, `getObjectDescription`,
`AlterForeignDataWrapper`, etc. Once `pg_foreign_data_wrapper` (OID
2328, Step 3bb) is reachable and the name index (OID 548, Step 3bc) is
seeded, the second deferred companion from Step 3bb is the last
remaining FDW catalog blocker before PG startup advances past
`InitPostgres`. Without a pg_class row for OID 112, PG's
`RelationIdGetRelation(112) → ScanPgRelation(112)` returns NULL during
syscache init and the backend FATALs.

## Fix

Pure catalog-seed addition — no encoder, builder, or `Init` flow
change. Mirrors the single-column `oid_ops` UNIQUE PKEY pattern
already used by:

- `pg_extension_oid_index`         (OID 3080, Step 3ax)
- `pg_event_trigger_oid_index`     (OID 3468, Step 3at)
- `pg_default_acl_oid_index`       (OID  828, Step 3am)
- `pg_conversion_oid_index`        (OID 2670, Step 3ai)
- `pg_cast_oid_index`              (OID 2660, Step 3ab)
- `pg_collation_oid_index`         (OID 3085, Step 3af)
- `pg_enum_oid_index`              (OID 3502, Step 3ao)
- `pg_opclass_oid_index`           (OID 2687, Step 3l)

### a) `internal/initdb/initdb.go::pgIndexInitialEntries`

New `entry(112, 2328, []int16{1}, []uint32{oidOps}, []uint32{0}, true,
true)` appended right after the Step 3bc entry for OID 548:

| field      | value                       | source |
|------------|-----------------------------|--------|
| OID        | 112                         | `pg_foreign_data_wrapper.h:55` |
| indrelid   | 2328 (pg_foreign_data_wrapper) | same |
| indkey     | `{1}` = `oid`               | `pg_foreign_data_wrapper_d.h` attnums (1=oid, 2=fdwname) |
| indclass   | `{oid_ops}`                 | declared `btree(oid oid_ops)` |
| collation  | `{0}`                       | `oid_ops` carries no collation — same as Steps 3ax/3at/3am/3ai/3ab/3af/3ao/3l |
| unique     | true                        | `DECLARE_UNIQUE_INDEX_PKEY` |
| primary    | true                        | `DECLARE_UNIQUE_INDEX_PKEY` (not `DECLARE_UNIQUE_INDEX`) |

### b) `internal/initdb/relcache_init.go::nailedLocalRels`

idxSpec gains `{112, "pg_foreign_data_wrapper_oid_index"}`.
`flattenRels` consults `pgIndexNattsByOID()` (returns 1 for OID 112),
so the nailed rel carries `RelKind='i', RelNatts=1` and PG's
`RelationInitIndexAccessInfo` `relnatts == indnatts` check at
`relcache.c:1492` passes.

### c) Empty-btree placeholder OID lists

All three loops in `bootstrapPostgresDatabase` (`base/1/`, `base/5/`,
`global/`) gain `112, // pg_foreign_data_wrapper_oid_index (Step 3bd)`.
The Step-3k empty btree (`btm_root = P_NONE`) is correct because
`pg_foreign_data_wrapper` is currently unpopulated — any
`SearchSysCache1(FOREIGNDATAWRAPPEROID, …)` probe correctly returns
no row.

### Plumbing flow-through (unchanged)

The seed threads through the existing pipelines without code change:

```
bootstrapPgClassTuples            → Form_pg_class row for OID 112
bootstrapPgAttributeTuples        → 1 row for oid attnum=1
bootstrapPgIndexTuples            → Form_pg_index row, captures TID in
                                    pgIndexTIDs map
bootstrapPgIndexIndexrelidIndex   → leaf in populated 2-page btree at
                                    file 2679
bootstrapPgClassOidIndex          → leaf at file 2662
bootstrapPgAttributeRelidAttnumIndex
                                  → composite leaf at file 2659
```

## Tests

### New (file: `internal/initdb/pg_foreign_data_wrapper_oid_index_test.go`)

- `TestPgForeignDataWrapperOidIndexSeededFromInitialEntries` — pins
  `(IndRelid=2328, IndKey=[1], IsUnique=true, IsPrimary=true,
  IndCollation=[0])`.
- `TestNailedLocalRelsContainsPgForeignDataWrapperOidIndex` — pins
  `(RelName="pg_foreign_data_wrapper_oid_index", RelKind='i',
  RelNatts=1)`.

### Extended

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` — adds `112: {1}` to
  the authoritative map. Strict count guard forces future additions
  to update the map.
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  — adds `112` so the populated 2679 btree must include this OID's
  leaf.

## Verification

```
go build ./...                                                          # PASS
go test -count=1 -run 'TestPgForeignDataWrapperOidIndex|…' ./internal/initdb/
                                                                        # PASS
go test -count=1 ./internal/initdb/                                     # 14 pre-existing
                                                                        # baseline failures
                                                                        # unchanged
go test -count=1 ./internal/executor/ ./internal/server/ \
                ./internal/storage/ ./internal/catalog/ \
                ./internal/mvcc/                                        # PASS
```

The 14 pre-existing initdb failures (Migration*, Create*, Committed*,
Runtime*, Multiple*, System*, Bootstrapped*, OpenOldCluster*,
SynchronousCommit*) are unchanged from Step 3bc — no new regressions
introduced. They are tracked separately under M0106-0011 / M0106-0012
/ M0106-0013.

## Next-blocker forecast

With Steps 3bb (heap), 3bc (name index), and 3bd (oid index) all
landed, the pg_foreign_data_wrapper catalog triple is fully seeded —
no further FDW-related OID is expected to surface from
`InitPostgres`. The next E2E re-run should advance past the FDW
catcache init and surface a different missing OID. That OID's
identification belongs to Step 3be scope.
