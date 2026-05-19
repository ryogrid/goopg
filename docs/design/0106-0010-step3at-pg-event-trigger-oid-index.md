# M0106-0010 Step 3at — pg_event_trigger_oid_index

## Blocker

After Step 3as seeded `pg_event_trigger_evtname_index` (OID 3467), the next
E2E re-run of `TestE2E_FailoverGoopgToPG/async`
(`GOOPG_RUN_BLOCKED_M0102_E2E=1`) produces a new PG-standby boot blocker:

```
FATAL:  could not open relation with OID 3468
```

`postgres/src/include/catalog/pg_event_trigger.h:55` declares:

```c
DECLARE_UNIQUE_INDEX_PKEY(pg_event_trigger_oid_index, 3468,
    EventTriggerOidIndexId, pg_event_trigger,
    btree(oid oid_ops));
MAKE_SYSCACHE(EVENTTRIGGEROID, pg_event_trigger_oid_index, 8);
```

PG's `RelationBuildDesc(3468) → ScanPgRelation(3468)` returns no row because
goopg's `nailedLocalRels` has never seeded a `pg_class` row for the
oid index, and no `pg_index` row exists for the index either.

## Root cause

Catalog-seed omission. The Step 3as note explicitly deferred the companion
PRIMARY-KEY index "to the next step — the next E2E re-run after this fix
surfaces `FATAL: could not open relation with OID 3468`". That is exactly
the failure we now resolve.

## Fix

Pure catalog-seed addition. No encoder, builder, or `Init` flow change.

1. `internal/initdb/initdb.go::pgIndexInitialEntries` gains
   `entry(3468, 3466, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)`
   — `oid` is attnum 1 of pg_event_trigger; `oid_ops` carries no
   collation. UNIQUE PRIMARY matches `DECLARE_UNIQUE_INDEX_PKEY`.
   Same single-column oid PKEY pattern as `pg_enum_oid_index` (OID 3502,
   Step 3ao), `pg_cast_oid_index` (OID 2660, Step 3ab),
   `pg_collation_oid_index` (OID 3085, Step 3af),
   `pg_conversion_oid_index` (OID 2670, Step 3ai),
   `pg_default_acl_oid_index` (OID 828, Step 3am), and
   `pg_opclass_oid_index` (OID 2687, Step 3l).

2. `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec list gains
   `{3468, "pg_event_trigger_oid_index"}`. `flattenRels` consults
   `pgIndexNattsByOID()` (returns 1 for OID 3468), so the nailed rel
   carries `RelKind='i', RelNatts=1` and
   `RelationInitIndexAccessInfo`'s `relnatts == indnatts` check passes
   (relcache.c:1492).

3. Three empty-placeholder OID lists in `bootstrapPostgresDatabase`
   (`base/1/`, `base/5/`, `global/`) gain
   `3468, // pg_event_trigger_oid_index (Step 3at)`. The placeholder
   is a valid empty PG18 btree metapage (Step 3k's `makeBtreeRootPage`
   writes `btm_root = P_NONE`), which is correct because
   `pg_event_trigger` itself is empty (no event triggers are
   bootstrapped) — any future `SearchSysCache1(EVENTTRIGGEROID, …)`
   probe will correctly return no row.

The seed threads automatically through the existing flow:
`bootstrapPgClassTuples` writes the Form_pg_class row →
`bootstrapPgAttributeTuples` writes the attribute row →
`bootstrapPgIndexTuples` writes the Form_pg_index row and captures
the TID in `pgIndexTIDs` →
`bootstrapPgIndexIndexrelidIndex` adds the leaf to the populated
2-page btree at file 2679 →
`bootstrapPgClassOidIndex` adds the leaf at file 2662 →
`bootstrapPgAttributeRelidAttnumIndex` adds the composite-key leaf
at file 2659.

After Step 3at the entire `pg_event_trigger.h` index pair (3467
evtname + 3468 oid) is seeded; subsequent next-blocker hunts move
to other catalog OIDs that the standby's syscache initialisation
sweeps.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestPgEventTriggerOidIndex|TestPgEventTriggerEvtnameIndex|TestNailedLocalRelsContainsPgEventTrigger|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3as (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

## Regression pins

- `TestPgEventTriggerOidIndexSeededFromInitialEntries` — pins
  `(IndRelid=3466, IndKey=[1], IsUnique=true, IsPrimary=true,
  IndCollation=[0])` for OID 3468 against the authoritative
  `pg_event_trigger.h:55` declaration.
- `TestNailedLocalRelsContainsPgEventTriggerOidIndex` — pins
  `RelName="pg_event_trigger_oid_index", RelKind='i', RelNatts=1`.
- `TestPgIndexInitialEntriesIndkeyMatchesPG18` — extended with
  `3468: {1}` (strict count guard forces future additions to update
  the pinned map).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  — extended with 3468 so the populated 2679 btree must include
  this OID's leaf.

## References

- `postgres/src/include/catalog/pg_event_trigger.h:55` — index
  declaration.
- `postgres/src/include/catalog/pg_event_trigger_d.h` —
  `EventTriggerOidIndexId = 3468`.
- `docs/design/0106-0010-step3as-pg-event-trigger-evtname-index.md` —
  immediate predecessor (companion evtname index).
- `docs/design/0106-0010-step3ar-pg-event-trigger-nailed-rel.md` —
  pg_event_trigger heap rel seed.
