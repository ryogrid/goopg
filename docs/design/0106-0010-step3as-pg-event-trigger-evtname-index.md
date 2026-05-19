# M0106-0010 Step 3as — pg_event_trigger_evtname_index

## Blocker

After Step 3ar seeded `pg_event_trigger` (OID 3466, the heap rel), the next
E2E re-run of `TestE2E_FailoverGoopgToPG/async`
(`GOOPG_RUN_BLOCKED_M0102_E2E=1`) produces a new PG-standby boot blocker:

```
FATAL:  could not open relation with OID 3467
```

`postgres/src/include/catalog/pg_event_trigger.h:54` declares:

```c
DECLARE_UNIQUE_INDEX(pg_event_trigger_evtname_index, 3467,
    EventTriggerNameIndexId, pg_event_trigger,
    btree(evtname name_ops));
MAKE_SYSCACHE(EVENTTRIGGERNAME, pg_event_trigger_evtname_index, 8);
```

PG's `RelationBuildDesc(3467) → ScanPgRelation(3467)` returns no row because
goopg's `nailedLocalRels` has never seeded a `pg_class` row for the
evtname index, and no `pg_index` row exists for the index either.

## Root cause

Catalog-seed omission. The Step 3ar note explicitly deferred the two
pg_event_trigger indexes "until a `MAKE_SYSCACHE(EVENTTRIGGER{NAME,OID},
…)` lookup surfaces as the next concrete blocker." The first such
lookup (against `EVENTTRIGGERNAME` = OID 3467) is what the next E2E
re-run trips on.

## Fix

Pure catalog-seed addition. No encoder, builder, or `Init` flow change.

1. `internal/initdb/initdb.go::pgIndexInitialEntries` gains
   `entry(3467, 3466, []int16{2}, []uint32{nameOps}, []uint32{cCollation},
   true, false)` — `evtname` is a `name` column at attnum 2 of
   pg_event_trigger; `name_ops` carries `C_COLLATION_OID = 950` (same
   convention as `pg_namespace_nspname_index` OID 2684, Step 3t, and
   `pg_conversion_name_nsp_index` OID 2669, Step 3aj). UNIQUE non-PRIMARY
   matches `DECLARE_UNIQUE_INDEX` (not `_PKEY`).

2. `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec list
   gains `{3467, "pg_event_trigger_evtname_index"}`. `flattenRels`
   consults `pgIndexNattsByOID()` (returns 1 for OID 3467), so the
   nailed rel carries `RelKind='i', RelNatts=1` and
   `RelationInitIndexAccessInfo`'s `relnatts == indnatts` check passes
   (relcache.c:1492).

3. Three empty-placeholder OID lists in `bootstrapPostgresDatabase`
   (`base/1/`, `base/5/`, `global/`) gain `3467, //
   pg_event_trigger_evtname_index (Step 3as)`. The placeholder is a
   valid empty PG18 btree metapage (Step 3k's `makeBtreeRootPage`
   writes `btm_root = P_NONE`), which is correct because
   `pg_event_trigger` itself is empty (no event triggers are
   bootstrapped) — any future `SearchSysCache1(EVENTTRIGGERNAME, …)`
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

The companion `pg_event_trigger_oid_index` (OID 3468, UNIQUE PRIMARY
on `oid oid_ops`, backing `MAKE_SYSCACHE(EVENTTRIGGEROID, …)`) is
deliberately deferred to the next step — the next E2E re-run after
this fix surfaces `FATAL: could not open relation with OID 3468`,
matching the same single-OID seed pattern.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestPgEventTriggerEvtnameIndex|TestNailedLocalRelsContainsPgEventTriggerEvtnameIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestNailedLocalRelsContainsPgEventTrigger'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3ar (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async`
  advances past `could not open relation with OID 3467` to the next
  blocker: `FATAL: could not open relation with OID 3468`
  (pg_event_trigger_oid_index, Step 3at territory).

## Regression pins

- `TestPgEventTriggerEvtnameIndexSeededFromInitialEntries` — pins
  `(IndRelid=3466, IndKey=[2], IsUnique=true, IsPrimary=false,
  IndCollation=[950])` for OID 3467 against the authoritative
  `pg_event_trigger.h:54` declaration.
- `TestNailedLocalRelsContainsPgEventTriggerEvtnameIndex` — pins
  `RelName="pg_event_trigger_evtname_index", RelKind='i', RelNatts=1`.
- `TestPgIndexInitialEntriesIndkeyMatchesPG18` — extended with
  `3467: {2}` (strict count guard forces future additions to update
  the pinned map).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  — extended with 3467 so the populated 2679 btree must include
  this OID's leaf.

## References

- `postgres/src/include/catalog/pg_event_trigger.h:54` — index
  declaration.
- `postgres/src/include/catalog/pg_event_trigger_d.h` —
  `EventTriggerNameIndexId = 3467`.
- `docs/design/0106-0010-step3ar-pg-event-trigger-nailed-rel.md` —
  immediate predecessor (heap rel seed).
