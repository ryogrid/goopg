# M0106-0010 Step 3ay — pg_extension_name_index catalog seed

## Status

LANDED 2026-05-18.

## Problem

After Step 3ax (commit `f3404ec`) seeded `pg_extension_oid_index` (OID
3080), the anticipated next PG-standby boot blocker is:

```
FATAL: could not open relation with OID 3081
```

OID 3081 is the companion `pg_extension_name_index` declared at
`postgres/src/include/catalog/pg_extension.h:57`:

```c
DECLARE_UNIQUE_INDEX(pg_extension_name_index, 3081,
    ExtensionNameIndexId, pg_extension,
    btree(extname name_ops));
MAKE_SYSCACHE(EXTENSIONNAME, pg_extension_name_index, 2);
```

This index backs the `EXTENSIONNAME` syscache. Once `pg_extension`
(OID 3079, Step 3aw) is reachable, every syscache probe that resolves
an extension by name (e.g. `get_extension_oid`) traverses OID 3081 —
the empty Step-3k btree placeholder is enough to satisfy lookups while
goopg seeds no extensions, but the relcache entry for 3081 still has to
exist (no pg_class row → `RelationIdGetRelation(3081)` returns NULL →
FATAL).

## Fix

Pure catalog-seed addition — no encoder, builder, or `Init` flow
change. Mirrors the single-column `name_ops` UNIQUE-non-PKEY pattern
already used by:

- `pg_event_trigger_evtname_index` (OID 3467, Step 3as)
- `pg_namespace_nspname_index`     (OID 2684, Step 3t)
- `pg_conversion_name_nsp_index`'s leading `name_ops` slot
  (OID 2669, Step 3aj — multi-col but same collation semantics)

### a) `internal/initdb/initdb.go::pgIndexInitialEntries`

New `entry(3081, 3079, []int16{2}, []uint32{nameOps},
[]uint32{cCollation}, true, false)` appended right after the Step 3ax
entry for OID 3080:

| field      | value                          | source |
|------------|--------------------------------|--------|
| OID        | 3081                           | `pg_extension.h:57` |
| indrelid   | 3079 (pg_extension)            | same   |
| indkey     | `{2}` = `extname`              | `pg_extension_d.h` attnums (1=oid, 2=extname) |
| indclass   | `{name_ops}`                   | declared `btree(extname name_ops)` |
| collation  | `{C_COLLATION_OID = 950}`      | `name_ops` is collation-sensitive — same as Steps 3t/3as/3aj |
| unique     | true                           | `DECLARE_UNIQUE_INDEX` |
| primary    | false                          | NOT `DECLARE_UNIQUE_INDEX_PKEY` — companion oid PKEY 3080 already carries the primary flag |

### b) `internal/initdb/relcache_init.go::nailedLocalRels`

idxSpec gains `{3081, "pg_extension_name_index"}`. `flattenRels`
consults `pgIndexNattsByOID()` (returns 1 for OID 3081), so the nailed
rel carries `RelKind='i', RelNatts=1` and PG's
`RelationInitIndexAccessInfo` `relnatts == indnatts` check at
`relcache.c:1492` passes.

### c) Empty-btree placeholder OID lists

All three loops in `bootstrapPostgresDatabase` (`base/1/`, `base/5/`,
`global/`) gain `3081, // pg_extension_name_index (Step 3ay)`. The
Step-3k empty btree (`btm_root = P_NONE`) is correct because
`pg_extension` is currently unpopulated — any
`SearchSysCache1(EXTENSIONNAME, …)` probe correctly returns no row.

### Plumbing flow-through (unchanged)

The seed threads through the existing pipelines without code change:

```
bootstrapPgClassTuples            → Form_pg_class row for OID 3081
bootstrapPgAttributeTuples        → 1 row for extname attnum=2
bootstrapPgIndexTuples            → Form_pg_index row, captures TID in
                                    pgIndexTIDs map
bootstrapPgIndexIndexrelidIndex   → leaf in populated 2-page btree at
                                    file 2679
bootstrapPgClassOidIndex          → leaf at file 2662
bootstrapPgAttributeRelidAttnumIndex
                                  → composite leaf at file 2659
```

## Tests

### New (file: `internal/initdb/pg_extension_name_index_test.go`)

- `TestPgExtensionNameIndexSeededFromInitialEntries` — pins
  `(IndRelid=3079, IndKey=[2], IsUnique=true, IsPrimary=false,
  IndCollation=[950])`.
- `TestNailedLocalRelsContainsPgExtensionNameIndex` — pins
  `(RelName="pg_extension_name_index", RelKind='i', RelNatts=1)`.

### Extended

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` — adds `3081: {2}` to
  the authoritative map. Strict count guard forces future additions to
  update the map.
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  — adds `3081` so the populated 2679 btree must include this OID's
  leaf.

## Verification

```
go build ./...                                              # PASS
go test -count=1 -run 'TestPgExtensionNameIndex|...'        # PASS
go test -count=1 ./internal/initdb/                         # 14 pre-existing
                                                            # baseline failures
                                                            # unchanged
go test -count=1 ./internal/executor/ ./internal/server/ \
                ./internal/storage/ ./internal/catalog/ \
                ./internal/mvcc/                            # PASS
```

The 14 pre-existing initdb failures (Migration*, Create*, Committed*,
Runtime*, Multiple*, System*, Bootstrapped*, OpenOldCluster*,
SynchronousCommit*) are unchanged from Step 3ax — no new regressions
introduced.

## Carry-over / next blocker

The next likely PG-standby boot blocker is the next missing nailed
relation surfaced by `RelationCacheInitializePhase3` after Step 3ay
populates the `EXTENSIONNAME` syscache backing. Candidates include
other syscaches whose backing index has neither a `pgIndexInitialEntries`
row nor a `nailedLocalRels` entry (e.g. `pg_foreign_data_wrapper`,
`pg_foreign_server`, `pg_foreign_table`, `pg_user_mapping`). The E2E
re-run (`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`)
will surface the concrete next OID; Step 3az addresses it.
