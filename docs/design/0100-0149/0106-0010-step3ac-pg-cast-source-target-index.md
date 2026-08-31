# M0106-0010 Step 3ac — pg_cast_source_target_index nailed-index seed

## Context

Step 3ab landed `pg_cast_oid_index` (OID 2660, UNIQUE PRIMARY KEY on the
single `oid` attnum). The carry-over from Steps 3aa/3ab named OID 2661
(`pg_cast_source_target_index`) as Step 3ac territory — pg_cast's
companion UNIQUE non-primary 2-column btree.

The anticipated next PG-standby boot blocker is
`could not open relation with OID 2661`, surfaced by
`RelationIdGetRelation(2661)` once Step 3ab cleared the 2660 FATAL.

## Authoritative source

```c
// postgres/src/include/catalog/pg_cast.h:60
DECLARE_UNIQUE_INDEX(pg_cast_source_target_index, 2661,
    CastSourceTargetIndexId, pg_cast,
    btree(castsource oid_ops, casttarget oid_ops));
```

UNIQUE but NOT PRIMARY (`DECLARE_UNIQUE_INDEX`, not the `_PKEY`
variant). 2-column composite key on `(castsource attnum=2,
casttarget attnum=3)` per `pg_cast.h:35-36`.

## Fix

Pure catalog-seed addition — no encoder, builder, or `Init` flow change.
Mirrors Step 3ab (single-OID pattern) but with a 2-column composite key.

### (a) `internal/initdb/initdb.go::pgIndexInitialEntries`

```go
entry(2661, 2605, []int16{2, 3}, []uint32{oidOps, oidOps},
    []uint32{0, 0}, true, false), // pg_cast_source_target_index
```

`IsUnique=true, IsPrimary=false` matches `DECLARE_UNIQUE_INDEX` (no
`_PKEY` suffix). `IndKey=[2,3]` reflects pg_cast attnums for
(castsource, casttarget).

### (b) `internal/initdb/relcache_init.go::nailedLocalRels`

```go
{2661, "pg_cast_source_target_index"},
```

`flattenRels` consults `pgIndexNattsByOID()` and derives
`RelKind='i', RelNatts=2`, so `RelationInitIndexAccessInfo`'s
`relnatts == indnatts` check (`relcache.c:1492`) passes for the
2-column composite key.

### (c) Three placeholder OID lists in `bootstrapPostgresDatabase`

The three OID lists at `initdb.go` (`base/1/`, `base/5/`, `global/`)
gain `2661, // pg_cast_source_target_index (Step 3ac)`. The Step-3k
empty btree metapage placeholder (`btm_root = P_NONE`) is sufficient
because `pg_cast` itself is currently empty — a zero-row index lookup
returns no false matches.

## Automatic plumbing flow

The single seed threads through the existing bootstrap unchanged:

1. `bootstrapPgClassTuples` writes the `Form_pg_class` row for OID 2661.
2. `bootstrapPgAttributeTuples` writes the 2 per-key `pg_attribute`
   rows (indexKeyAttrs returns 2 typed-oid columns).
3. `bootstrapPgIndexTuples` writes the `Form_pg_index` row (with
   `indnatts=2`) and captures the heap TID in `pgIndexTIDs[2661]`.
4. `bootstrapPgIndexIndexrelidIndex` adds OID 2661's leaf to the
   populated 2-page btree at file 2679, sorted ascending by indexrelid.
5. `bootstrapPgClassOidIndex` adds OID 2661's leaf at file 2662.
6. `bootstrapPgAttributeRelidAttnumIndex` adds the 2 composite-key
   leaves at file 2659.
7. `writeRelcacheInitFile` emits a `Form_pg_class` + 2
   `Form_pg_attribute` blob group for OID 2661.

The relfile at `base/{1,5}/2661 + global/2661` stays the Step-3k
empty-btree placeholder because goopg has no
2-column-oid composite-key IndexTuple builder yet; populating the file
is not load-bearing for resolving the FATAL, which is a `pg_class`
lookup failure rather than a tuple-lookup miss.

## Regression pins

`internal/initdb/pg_cast_source_target_index_test.go`:

- `TestPgCastSourceTargetIndexSeededFromInitialEntries` — asserts
  `(IndRelid=2605, IndKey=[2 3], IsUnique=true, IsPrimary=false)`.
- `TestNailedLocalRelsContainsPgCastSourceTargetIndex` — asserts
  `RelName="pg_cast_source_target_index", RelKind='i', RelNatts=2`.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` — adds `2661: {2, 3}` to
  the authoritative map (strict `len(got) == len(want)` count check
  auto-rejects future additions that forget the map update).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  — adds 2661 so the populated 2679 btree must carry this leaf.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestPgCastSourceTargetIndex|TestNailedLocalRelsContainsPgCastSourceTargetIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestNailedLocalRelsContainsPgCast|TestPgCastOidIndex|TestPgAggregateFnoidIndex|TestPgAmopFamStrat'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3ab (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.

## Next blocker

Expected to surface on the next E2E re-run. Likely candidates as
relcache initialization continues to walk the catalog list:

- Other `pg_cast_*` consumers if any remain unseeded (none expected —
  2660+2661 cover the canonical `pg_cast.h` index declarations); or
- A different local/shared catalog OID that PG's
  `RelationCacheInitializePhase3` reaches next (e.g. pg_constraint,
  pg_conversion families). Handled by the next ad-hoc Step (3ad).

## Files

- `internal/initdb/initdb.go` (entry + 3 placeholder list updates)
- `internal/initdb/relcache_init.go` (nailedLocalRels idxSpec)
- `internal/initdb/pg_cast_source_target_index_test.go` (new regression pins)
- `internal/initdb/pg_index_indkey_test.go` (map extended)
- `internal/initdb/btree_index_bootstrap_test.go` (mustHave extended)
