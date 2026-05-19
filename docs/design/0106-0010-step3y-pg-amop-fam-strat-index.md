# M0106-0010 Step 3y — Seed `pg_amop_fam_strat_index` (OID 2653)

Status: LANDED 2026-05-18

## Symptom

After Step 3x landed the `pg_aggregate_fnoid_index` seed, the next
`GOOPG_RUN_BLOCKED_M0102_E2E=1 TestE2E_FailoverGoopgToPG/async` re-run
reached `PM_HOT_STANDBY` cleanly, but every newly forked client backend
FATAL'd in a tight retry loop:

```
FATAL:  could not open relation with OID 2653
```

emitted from `postgres/src/backend/access/common/relation.c:61`
(`RelationBuildDesc(2653) → ScanPgRelation(2653)` returns no row, so
`elog(ERROR, "could not open relation with OID %u", relid)` fires).

## Root cause

OID 2653 is `pg_amop_fam_strat_index`, declared at
`postgres/src/include/catalog/pg_amop.h:90`:

```c
DECLARE_UNIQUE_INDEX(pg_amop_fam_strat_index, 2653,
    AccessMethodStrategyIndexId, pg_amop,
    btree(amopfamily oid_ops, amoplefttype oid_ops,
          amoprighttype oid_ops, amopstrategy int2_ops));
MAKE_SYSCACHE(AMOPSTRATEGY, pg_amop_fam_strat_index, 64);
```

Steps 3c and 3h seeded the `pg_amop` heap rows and the cross-type
strategy operator rows, but the *index* OID 2653 itself was never added
to `pgIndexInitialEntries()` or `nailedLocalRels`. That meant:

1. No `Form_pg_index` heap row was emitted for 2653, so the populated
   2-page btree at file 2679 (`pg_index_indexrelid_index`) lacked a
   leaf for it.
2. No `Form_pg_class` heap row was emitted for 2653, so the populated
   btree at file 2662 (`pg_class_oid_index`) also lacked a leaf.

Earlier blockers (Steps 3a–3x) crashed each backend before the
`AMOPSTRATEGY` syscache lookup path was exercised. Step 3x removed the
last upstream FATAL on that path, exposing this latent gap.

## Fix

Pure catalog-seed addition — no encoder, builder, or `Init` flow change.

1. `internal/initdb/initdb.go::pgIndexInitialEntries` gains:

   ```go
   entry(2653, 2602, []int16{2, 3, 4, 5},
       []uint32{oidOps, oidOps, oidOps, int2Ops},
       []uint32{0, 0, 0, 0}, true, false) // pg_amop_fam_strat_index
   ```

   `amopfamily/amoplefttype/amoprighttype/amopstrategy` are pg_amop
   attnums 2/3/4/5 per `pg_amop_d.h`. UNIQUE, NOT primary
   (`DECLARE_UNIQUE_INDEX` is not the `_PKEY` variant).

2. `internal/initdb/relcache_init.go::nailedLocalRels` idxSpec gains
   `{2653, "pg_amop_fam_strat_index"}`. `flattenRels` consults
   `pgIndexNattsByOID()` (returns 4 for OID 2653), so the synthesised
   nailed pg_class row carries `RelKind='i', RelNatts=4` and
   `RelationInitIndexAccessInfo`'s `relnatts == indnatts` check at
   `relcache.c:1492` passes.

3. The three empty-placeholder OID lists in `bootstrapPostgresDatabase`
   (`base/1/`, `base/5/`, `global/`) gain `2653, // pg_amop_fam_strat_index
   (Step 3y)`. PG's `mdopen` finds a valid empty-btree file before any
   later populated overwrite. (The 4-column composite key encoder is
   not yet implemented in goopg, so 2653's file stays an empty Step-3k
   placeholder — sufficient because PG only opens the relcache entry
   and uses the index via `MAKE_SYSCACHE(AMOPSTRATEGY, …)` paths that
   tolerate an empty index returning zero rows during initial standby
   boot; no upstream code requires a populated 2653 to advance past
   this FATAL.)

The seed threads automatically through the existing flow:
`bootstrapPgClassTuples → bootstrapPgAttributeTuples →
bootstrapPgIndexTuples (writes Form_pg_index row, captures TID in
pgIndexTIDs map) → bootstrapPgIndexIndexrelidIndex (adds 25th leaf to
populated 2-page btree at file 2679) → bootstrapPgClassOidIndex (adds
leaf at file 2662) → bootstrapPgAttributeRelidAttnumIndex (adds
composite-key leaves at file 2659)`.

## Regression pins

`internal/initdb/pg_amop_fam_strat_index_test.go` (new):

- `TestPgAmopFamStratIndexSeededFromInitialEntries` — asserts
  `(IndRelid=2602, IndKey=[2,3,4,5], IsUnique=true, IsPrimary=false)`.
- `TestNailedLocalRelsContainsPgAmopFamStratIndex` — asserts
  `RelName="pg_amop_fam_strat_index", RelKind='i', RelNatts=4`.

Existing pins extended in-place:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` adds `2653: {2,3,4,5}` to
  the authoritative map; the strict `len(got) != len(want)` count
  guard forces any future addition to update the map.
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  gains `2653` so the populated 2679 btree is required to carry this
  leaf.

## Verification

```
go test -count=1 -run \
  'TestPgAmopFamStrat|TestNailedLocalRelsContainsPgAmopFamStrat|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestNailedLocalRelsContainsPgAggregateFnoidIndex|TestPgAggregateFnoidIndex' \
  ./internal/initdb/
# PASS

go test -count=1 ./internal/initdb/
# Same 14 pre-existing baseline failures as Step 3x
# (TestMigration*, TestCreate*, TestBootstrappedPG*,
#  TestSynchronousCommitFlushesByDefault,
#  TestOpenOldClusterWithoutM0030*,
#  TestSystemCatalogRelfilesAreValidHeapPages,
#  TestCommittedTableSurvivesCrashRestart,
#  TestRuntimeCloseTriggersFinalCheckpoint,
#  TestMultipleTablesLoadFromHeap). No new regressions.

go test -count=1 ./internal/executor/ ./internal/server/ \
                  ./internal/storage/ ./internal/catalog/ \
                  ./internal/mvcc/
# PASS

GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -count=1 -timeout=420s \
  -run 'TestE2E_FailoverGoopgToPG/async' ./internal/testport/
# Advances past "could not open relation with OID 2653" to the next
# blocker: "could not open relation with OID 2694" (Step 3z territory;
# likely pg_constraint_conrelid_contypid_conname_index or similar).
```

## Next blocker

`FATAL: could not open relation with OID 2694` — same pattern, another
nailed index missing from `pgIndexInitialEntries`/`nailedLocalRels`.
Step 3z scope.
