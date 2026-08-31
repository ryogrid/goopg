# M0106-0010 Step 3ax — Seed `pg_extension_oid_index` (OID 3080)

Date: 2026-05-18
Milestone: M0106-0010 — Resolve array assertion and bootstrap pg_am(+related) tuples
Predecessor: Step 3aw — pg_extension nailed rel (OID 3079)
Successor (anticipated): Step 3ay — pg_extension_name_index (OID 3081, UNIQUE non-PKEY on `extname name_ops`)

## Problem

After Step 3aw landed the pg_extension heap (OID 3079) as a nailed local rel,
the next anticipated PG-standby boot blocker is:

```
FATAL: could not open relation with OID 3080
```

OID 3080 is the primary-key btree index over pg_extension.oid, declared in
`postgres/src/include/catalog/pg_extension.h:56`:

```
DECLARE_UNIQUE_INDEX_PKEY(pg_extension_oid_index, 3080,
    ExtensionOidIndexId, pg_extension, btree(oid oid_ops));
MAKE_SYSCACHE(EXTENSIONOID, pg_extension_oid_index, 2);
```

Without a pg_class row for OID 3080, PG's
`RelationIdGetRelation(3080) → ScanPgRelation(3080)` returns NULL during
syscache init and the backend FATALs.

## Fix

Pure catalog-seed addition. This mirrors the single-column oid PKEY pattern
established by Steps 3l (pg_opclass_oid_index), 3ab (pg_cast_oid_index),
3af (pg_collation_oid_index), 3ai (pg_conversion_oid_index),
3am (pg_default_acl_oid_index), 3ao (pg_enum_oid_index), and
3at (pg_event_trigger_oid_index). No encoder, builder, or `Init` flow change.

### Changes

1. `internal/initdb/initdb.go::pgIndexInitialEntries`
   gains `entry(3080, 3079, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)`
   — UNIQUE PRIMARY single oid_ops key (no collation) over pg_extension heap
   OID 3079 (Step 3aw nailed rel).

2. `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec gains
   `{3080, "pg_extension_oid_index"}`. `flattenRels` + `pgIndexNattsByOID`
   derives `RelKind='i', RelNatts=1` so the `relnatts==indnatts` check
   (relcache.c:1492) passes.

3. The three empty-btree-placeholder OID lists in
   `bootstrapPostgresDatabase` (`base/1/`, `base/5/`, `global/`) gain
   `3080, // pg_extension_oid_index (Step 3ax)`. The Step 3k empty-btree
   placeholder is sufficient because pg_extension is currently unpopulated —
   any `SearchSysCache1(EXTENSIONOID, …)` probe correctly returns no row.

### Bootstrap-flow threading

The seed threads automatically through the existing flow:

```
bootstrapPgClassTuples (Form_pg_class row)
  → bootstrapPgAttributeTuples (1 pg_attribute row for the indexed oid column)
  → bootstrapPgIndexTuples (captures TID for 3080)
  → bootstrapPgIndexIndexrelidIndex (adds leaf at file 2679)
  → bootstrapPgClassOidIndex (adds leaf at 2662)
  → bootstrapPgAttributeRelidAttnumIndex (composite leaf at 2659)
```

The companion index 3081 (`pg_extension_name_index`, UNIQUE non-PKEY on
`extname name_ops`) is deferred to Step 3ay — same deferral pattern as
the evtname/oid pair (Steps 3as/3at) for pg_event_trigger.

## Tests

New regression pins in `internal/initdb/pg_extension_oid_index_test.go`:

- `TestPgExtensionOidIndexSeededFromInitialEntries` — asserts the
  pgIndexInitialEntries entry: `IndRelid=3079, IndKey=[1], IsUnique=true,
  IsPrimary=true, IndCollation=[0]`.
- `TestNailedLocalRelsContainsPgExtensionOidIndex` — asserts the nailedLocalRels
  entry: `RelName="pg_extension_oid_index", RelKind='i', RelNatts=1`.

Existing pin updates:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` — strict-count map gains
  `3080: {1}`. The strict-count guard rejects future additions without
  matching map updates.
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave` —
  extended with 3080 so the populated 2679 btree must carry this leaf.

## Verification

```
go build ./...                                                            # PASS
go test -count=1 -run \
  'TestPgExtensionOidIndex|TestNailedLocalRelsContainsPgExtensionOidIndex|
   TestPgEventTriggerOidIndex|TestNailedLocalRelsContainsPgExtension|
   TestPgIndexInitialEntriesIndkeyMatchesPG18|
   TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree|
   TestNailedIndexRelnattsAgreesWithIndnatts|
   TestPgIndexColDefsMatchesRelcacheAttrs|
   TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|
   TestBootstrapMappedLocalCatalogHeapsWritesEmptyHeapPages' \
  ./internal/initdb/                                                      # PASS
go test -count=1 ./internal/initdb/                                       # same 14 pre-existing baseline failures as Step 3aw — no new regressions
go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/
  ./internal/catalog/ ./internal/mvcc/                                    # PASS
```

The 14 pre-existing initdb failures (`TestMigration*`, `TestCreate*`,
`TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
`TestOpenOldClusterWithoutM0030*`, `TestSystemCatalogRelfilesAreValidHeapPages`,
`TestCommittedTableSurvivesCrashRestart`,
`TestRuntimeCloseTriggersFinalCheckpoint`, `TestMultipleTablesLoadFromHeap`)
are tracked separately under M0106-0011 / M0106-0012 / M0106-0013.

## Next-blocker forecast

After Step 3ax, the next OID a PG-standby will demand from pg_extension's
syscache pair is OID 3081 (`pg_extension_name_index`) — same
single-key + name_ops + collation pattern as Step 3t
(pg_namespace_nspname_index) and Step 3as (pg_event_trigger_evtname_index).
Step 3ay scope.
