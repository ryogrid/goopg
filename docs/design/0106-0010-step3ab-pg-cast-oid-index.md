# M0106-0010 Step 3ab — pg_cast_oid_index nailed-index seed

## Context

Step 3aa landed `pg_cast` (OID 2605) as a nailed local catalog: a 6-column
`Form_pg_class` heap row in `base/{1,5}/1259`, 6 `pg_attribute` heap rows
in `base/{1,5}/1249`, leaf entries in the populated `pg_class_oid_index`
(2662) and `pg_attribute_relid_attnum_index` (2659), and an empty
`InitPage`-stamped heap at `base/{1,5}/2605`.

The next PG-standby boot blocker (anticipated, per Step 3aa's carry-over
note) is `could not open relation with OID 2660` —
`pg_cast_oid_index` per
`postgres/src/include/catalog/pg_cast_d.h:24`
(`#define CastOidIndexId 2660`). The companion index OID 2661
(`pg_cast_source_target_index`) is intentionally deferred to keep the
single-OID rhythm established by Steps 3w → 3x → 3y → 3z → 3aa.

## Authoritative source

```c
// postgres/src/include/catalog/pg_cast.h:59
DECLARE_UNIQUE_INDEX_PKEY(pg_cast_oid_index, 2660, CastOidIndexId,
    pg_cast, btree(oid oid_ops));
```

UNIQUE PRIMARY KEY on attnum 1 (`oid` is `pg_cast`'s first column per
`pg_cast.h:34`). Indexes the `pg_cast` heap (OID 2605).

## Fix

Pure catalog-seed addition — no encoder, builder, or `Init` flow change.
Mirrors Steps 3x (`pg_aggregate_fnoid_index`) and 3w/3aa
single-attribute-oid pattern.

### (a) `internal/initdb/initdb.go::pgIndexInitialEntries`

```go
entry(2660, 2605, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_cast_oid_index
```

`IsUnique=true, IsPrimary=true` matches `DECLARE_UNIQUE_INDEX_PKEY`.
`IndKey=[1]` because `oid` is `pg_cast` attnum 1.

### (b) `internal/initdb/relcache_init.go::nailedLocalRels`

```go
{2660, "pg_cast_oid_index"},
```

`flattenRels` consults `pgIndexNattsByOID()` and derives
`RelKind='i', RelNatts=1`, so `RelationInitIndexAccessInfo`'s
`relnatts == indnatts` check (`relcache.c:1492`) passes.

### (c) Three placeholder OID lists in `bootstrapPostgresDatabase`

Lines 680, 764, 783 (`base/1/`, `base/5/`, `global/`) gain
`2660, // pg_cast_oid_index (Step 3ab)`. The placeholder is a valid
empty PG18 btree metapage (Step 3k's `makeBtreeRootPage` writes
`btm_root = P_NONE`) — correct because `pg_cast` is currently empty
(no cast functions are bootstrapped), so a zero-row index lookup is
the expected behavior.

## Automatic plumbing flow

The single seed threads through the existing bootstrap:

1. `bootstrapPgClassTuples` writes the `Form_pg_class` row for OID 2660.
2. `bootstrapPgAttributeTuples` writes the per-key `pg_attribute` row.
3. `bootstrapPgIndexTuples` writes the `Form_pg_index` row and captures
   the heap TID in `pgIndexTIDs[2660]`.
4. `bootstrapPgIndexIndexrelidIndex` adds OID 2660's leaf to the
   populated 2-page btree at file 2679, sorted ascending by indexrelid.
5. `bootstrapPgClassOidIndex` adds OID 2660's leaf at file 2662.
6. `bootstrapPgAttributeRelidAttnumIndex` adds the composite-key leaf
   at file 2659.
7. `writeRelcacheInitFile` emits a `Form_pg_class` + 1
   `Form_pg_attribute` blob group for OID 2660.

## Why companion OID 2661 is deferred

`pg_cast_source_target_index` (OID 2661, UNIQUE non-primary, btree on
(castsource, casttarget)) is a 2-column composite index. Its inclusion
mirrors this step verbatim (entry + nailedLocalRels + placeholder
lists). Splitting it into Step 3ac preserves the established
single-OID-per-step cadence, makes E2E blocker diagnosis cleaner, and
keeps the diff small. Step 3aa carry-over already named 2661 as Step
3ab/3ac territory.

## Regression pins

`internal/initdb/pg_cast_oid_index_test.go`:

- `TestPgCastOidIndexSeededFromInitialEntries` — asserts
  `(IndRelid=2605, IndKey=[1], IsUnique=true, IsPrimary=true)`.
- `TestNailedLocalRelsContainsPgCastOidIndex` — asserts
  `RelName="pg_cast_oid_index", RelKind='i', RelNatts=1`.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` — adds `2660: {1}` to
  the authoritative map (strict `len(got) == len(want)` count check
  auto-rejects future additions that forget the map update).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  — adds 2660 so the populated 2679 btree must carry this leaf.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run
  'TestPgCastOidIndex|TestNailedLocalRelsContainsPgCastOidIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestNailedLocalRelsContainsPgCast|TestPgAggregateFnoidIndex|TestPgAmopFamStrat'
  ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3aa (`TestMigration*`, `TestCreate*`,
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

Expected to surface on the next E2E re-run as one of:

- `could not open relation with OID 2661` — `pg_cast_source_target_index`
  (Step 3ac territory, follows verbatim from this step but with
  `IndKey=[2,3], IsUnique=true, IsPrimary=false`); or
- a different shared/local catalog OID that comes earlier in PG's
  relcache initialization order — handled by the next ad-hoc Step.

## Files

- `internal/initdb/initdb.go` (entry + 3 placeholder list updates)
- `internal/initdb/relcache_init.go` (nailedLocalRels idxSpec)
- `internal/initdb/pg_cast_oid_index_test.go` (new regression pins)
- `internal/initdb/pg_index_indkey_test.go` (map extended)
- `internal/initdb/btree_index_bootstrap_test.go` (mustHave extended)
