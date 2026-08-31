# M0106-0010 Step 3bc — pg_foreign_data_wrapper_name_index catalog seed

## Status

LANDED 2026-05-18.

## Problem

After Step 3bb (commit `04e6151`) seeded the pg_foreign_data_wrapper
nailed local rel (OID 2328), the next PG-standby boot blocker observed
under `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
is:

```
FATAL: could not open relation with OID 548
```

OID 548 is the companion `pg_foreign_data_wrapper_name_index` declared
at `postgres/src/include/catalog/pg_foreign_data_wrapper.h:56`:

```c
DECLARE_UNIQUE_INDEX(pg_foreign_data_wrapper_name_index, 548,
    ForeignDataWrapperNameIndexId, pg_foreign_data_wrapper,
    btree(fdwname name_ops));
MAKE_SYSCACHE(FOREIGNDATAWRAPPERNAME,
    pg_foreign_data_wrapper_name_index, 2);
```

This index backs the `FOREIGNDATAWRAPPERNAME` syscache. Once
`pg_foreign_data_wrapper` (OID 2328, Step 3bb) is reachable, every
syscache probe that resolves a foreign-data-wrapper by name (e.g.
`get_foreign_data_wrapper_oid`, `objectaddress.c:266`) traverses OID
548 — the empty Step-3k btree placeholder is enough to satisfy lookups
while goopg seeds no FDWs, but the relcache entry for 548 still has to
exist (no pg_class row → `RelationIdGetRelation(548)` returns NULL →
FATAL).

Note: the E2E test surfaces OID 548 (the name index) *before* OID 112
(the companion oid PKEY index) because `process_settings` and other
early backend startup paths reach FOREIGNDATAWRAPPERNAME first. Step
3bd will close OID 112 next.

## Fix

Pure catalog-seed addition — no encoder, builder, or `Init` flow
change. Mirrors the single-column `name_ops` UNIQUE-non-PKEY pattern
already used by:

- `pg_extension_name_index`         (OID 3081, Step 3ay)
- `pg_event_trigger_evtname_index`  (OID 3467, Step 3as)
- `pg_namespace_nspname_index`      (OID 2684, Step 3t)

### a) `internal/initdb/initdb.go::pgIndexInitialEntries`

New `entry(548, 2328, []int16{2}, []uint32{nameOps},
[]uint32{cCollation}, true, false)` appended right after the Step 3ay
entry for OID 3081:

| field      | value                       | source |
|------------|-----------------------------|--------|
| OID        | 548                         | `pg_foreign_data_wrapper.h:56` |
| indrelid   | 2328 (pg_foreign_data_wrapper) | same |
| indkey     | `{2}` = `fdwname`           | `pg_foreign_data_wrapper_d.h` attnums (1=oid, 2=fdwname) |
| indclass   | `{name_ops}`                | declared `btree(fdwname name_ops)` |
| collation  | `{C_COLLATION_OID = 950}`   | `name_ops` is collation-sensitive — same as Steps 3ay/3as/3t |
| unique     | true                        | `DECLARE_UNIQUE_INDEX` |
| primary    | false                       | NOT `DECLARE_UNIQUE_INDEX_PKEY` — companion oid PKEY 112 carries primary (Step 3bd) |

### b) `internal/initdb/relcache_init.go::nailedLocalRels`

idxSpec gains `{548, "pg_foreign_data_wrapper_name_index"}`. `flattenRels`
consults `pgIndexNattsByOID()` (returns 1 for OID 548), so the nailed
rel carries `RelKind='i', RelNatts=1` and PG's
`RelationInitIndexAccessInfo` `relnatts == indnatts` check at
`relcache.c:1492` passes.

### c) Empty-btree placeholder OID lists

All three loops in `bootstrapPostgresDatabase` (`base/1/`, `base/5/`,
`global/`) gain `548, // pg_foreign_data_wrapper_name_index (Step 3bc)`.
The Step-3k empty btree (`btm_root = P_NONE`) is correct because
`pg_foreign_data_wrapper` is currently unpopulated — any
`SearchSysCache1(FOREIGNDATAWRAPPERNAME, …)` probe correctly returns
no row.

### Plumbing flow-through (unchanged)

The seed threads through the existing pipelines without code change:

```
bootstrapPgClassTuples            → Form_pg_class row for OID 548
bootstrapPgAttributeTuples        → 1 row for fdwname attnum=2
bootstrapPgIndexTuples            → Form_pg_index row, captures TID in
                                    pgIndexTIDs map
bootstrapPgIndexIndexrelidIndex   → leaf in populated 2-page btree at
                                    file 2679
bootstrapPgClassOidIndex          → leaf at file 2662
bootstrapPgAttributeRelidAttnumIndex
                                  → composite leaf at file 2659
```

## Tests

### New (file: `internal/initdb/pg_foreign_data_wrapper_name_index_test.go`)

- `TestPgForeignDataWrapperNameIndexSeededFromInitialEntries` — pins
  `(IndRelid=2328, IndKey=[2], IsUnique=true, IsPrimary=false,
  IndCollation=[950])`.
- `TestNailedLocalRelsContainsPgForeignDataWrapperNameIndex` — pins
  `(RelName="pg_foreign_data_wrapper_name_index", RelKind='i',
  RelNatts=1)`.

### Extended

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` — adds `548: {2}` to
  the authoritative map. Strict count guard forces future additions to
  update the map.
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  — adds `548` so the populated 2679 btree must include this OID's
  leaf.

## Verification

```
go build ./...                                              # PASS
go test -count=1 -run 'TestPgForeignDataWrapperNameIndex|…' # PASS
go test -count=1 ./internal/initdb/                         # 14 pre-existing
                                                            # baseline failures
                                                            # unchanged
go test -count=1 ./internal/executor/ ./internal/server/ \
                ./internal/storage/ ./internal/catalog/ \
                ./internal/mvcc/                            # PASS
GOOPG_RUN_BLOCKED_M0102_E2E=1 \
  go test -run TestE2E_FailoverGoopgToPG/async \
  ./internal/testport/                                       # OID 548 blocker
                                                             # closed; next
                                                             # blocker: OID 112
                                                             # (Step 3bd)
```

The 14 pre-existing initdb failures (Migration*, Create*, Committed*,
Runtime*, Multiple*, System*, Bootstrapped*, OpenOldCluster*,
SynchronousCommit*) are unchanged from Step 3bb — no new regressions
introduced.

## Carry-over / next blocker

The E2E re-run after Step 3bc surfaces:

```
FATAL: could not open relation with OID 112
```

OID 112 = `pg_foreign_data_wrapper_oid_index` per
`postgres/src/include/catalog/pg_foreign_data_wrapper.h:55`:

```c
DECLARE_UNIQUE_INDEX_PKEY(pg_foreign_data_wrapper_oid_index, 112,
    ForeignDataWrapperOidIndexId, pg_foreign_data_wrapper,
    btree(oid oid_ops));
MAKE_SYSCACHE(FOREIGNDATAWRAPPEROID, pg_foreign_data_wrapper_oid_index, 2);
```

The second deferred companion index from Step 3bb. Step 3bd will seed
it via the same single-column `oid_ops` UNIQUE-PKEY pattern as Steps
3ax / 3at / 3am / 3ai / 3ab / 3af / 3ao / 3l.
