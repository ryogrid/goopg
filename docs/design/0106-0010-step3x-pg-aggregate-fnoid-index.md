# M0106-0010 Step 3x — pg_aggregate_fnoid_index (OID 2650) Catalog Seed

## Context

After Step 3w added `pg_aggregate` (OID 2600) to `nailedLocalRels` and seeded
empty heap pages for ~30 mapped local catalogs, the PG-standby boot advanced
past `FATAL: could not open relation with OID 2600` to the next blocker:

```
FATAL: could not open relation with OID 2650
```

Authoritative source — `postgres/src/include/catalog/pg_aggregate.h:113-115`:

```c
DECLARE_UNIQUE_INDEX_PKEY(pg_aggregate_fnoid_index, 2650,
    AggregateFnoidIndexId, pg_aggregate,
    btree(aggfnoid oid_ops));

MAKE_SYSCACHE(AGGFNOID, pg_aggregate_fnoid_index, 16);
```

PG's `RelationIdGetRelation(2650)` is invoked via the AGGFNOID syscache (or
directly during early-startup catalog probes) and walks
`pg_class_oid_index` (OID 2662 — populated since Step 3m). With no
`Form_pg_class` row for OID 2650, `ScanPgRelation` returns NULL and
`RelationBuildDesc` FATALs.

## Fix

Pure catalog-seed addition; no encoder, builder, or `Init`-flow change.
Mirrors Step 3t's pg_namespace pattern.

### `internal/initdb/initdb.go::pgIndexInitialEntries`

Append one entry at the end of the local-catalog section:

```go
entry(2650, 2600, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true), // pg_aggregate_fnoid_index
```

`aggfnoid` is `regproc` type but the canonical index uses `oid_ops`, not
`regproc_ops` (confirmed in `pg_aggregate.h:113`). `oidOps = 1981` is already
in `pgOpclassInitialEntries` (Step 3b). Single-column UNIQUE PRIMARY KEY.

### `internal/initdb/relcache_init.go::nailedLocalRels`

Append `{2650, "pg_aggregate_fnoid_index"}` to the `idxSpec` list.
`flattenRels` consults `pgIndexNattsByOID()` which now returns `1` for OID
2650, so the resulting `nailedRel` carries `RelKind='i', RelNatts=1`. PG's
`RelationInitIndexAccessInfo` relnatts/indnatts consistency check
(`relcache.c:1492`) sees `1 == 1` and passes.

### `internal/initdb/initdb.go` — three empty-placeholder OID lists

Each of the three `for _, oid := range []uint32{ ... }` loops at the bottom
of `bootstrapPostgresDatabase` (writing empty btree pages to `base/1/`,
`base/5/`, and `global/`) gains `2650, // pg_aggregate_fnoid_index (Step 3x)`
at the top of the list. The placeholder file is a valid empty PG18
btree metapage (Step 3k's `makeBtreeRootPage` writes `btm_magic`,
`BTREE_VERSION`, `btm_root = P_NONE`). Empty is semantically correct because
`pg_aggregate` itself is empty — no aggregate functions are bootstrapped.

## Plumbing flow

No code change required beyond the seeds. The existing flow handles the
heap and index automatically:

1. `bootstrapPgClassTuples` walks `nailedLocalRels`, writes a
   `Form_pg_class` row for OID 2650 to `base/{1,5}/1259`.
2. `bootstrapPgAttributeTuples` writes one `pg_attribute` row for the
   `oid` key column to `base/{1,5}/1249`.
3. `bootstrapPgIndexTuples` walks `pgIndexInitialEntries`, writes the
   `Form_pg_index` row for OID 2650 to `base/{1,5}/2610` and returns its
   heap TID via the `pgIndexTIDs` map.
4. `bootstrapPgIndexIndexrelidIndex` consumes the map, includes OID 2650's
   `(indexrelid → heap TID)` pair in the populated 2-page btree at
   `base/{1,5}/2679 + global/2679` so PG's `SearchSysCache1(INDEXRELID, 2650)`
   finds it.
5. `bootstrapPgClassOidIndex` includes OID 2650 in the populated 2-page
   btree at `base/{1,5}/2662 + global/2662`.
6. `bootstrapPgAttributeRelidAttnumIndex` includes the `(2650, 1)` pair in
   the populated composite-key btree at `base/{1,5}/2659 + global/2659`.

## Regression pins

`internal/initdb/pg_aggregate_fnoid_index_test.go`:

- `TestPgAggregateFnoidIndexSeededFromInitialEntries` — pins
  `(IndRelid=2600, IndKey=[1], IsUnique=true, IsPrimary=true)`.
- `TestNailedLocalRelsContainsPgAggregateFnoidIndex` — pins
  `(RelName="pg_aggregate_fnoid_index", RelKind='i', RelNatts=1)`.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` — adds `2650: {1}` to the
  authoritative map and (via the strict `len(got) != len(want)` count guard)
  forces future additions to update the map.
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave` —
  adds `2650` so the populated 2679 btree must include this OID's leaf.

## Verification

```
go test -count=1 -run \
  'TestPgAggregateFnoidIndex|TestNailedLocalRelsContainsPgAggregateFnoidIndex|TestPgIndexInitialEntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailedIndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKeyColumn|TestNailedLocalRelsContainsPgAggregate' \
  ./internal/initdb/
```

PASS.

```
go test -count=1 ./internal/initdb/
```

Same 14 pre-existing baseline failures as Step 3w (`TestMigration*`,
`TestCreate*`, `TestBootstrappedPG*`,
`TestSynchronousCommitFlushesByDefault`, `TestOpenOldClusterWithoutM0030*`,
`TestSystemCatalogRelfilesAreValidHeapPages`,
`TestCommittedTableSurvivesCrashRestart`,
`TestRuntimeCloseTriggersFinalCheckpoint`,
`TestMultipleTablesLoadFromHeap`) — no new regressions.

```
go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ \
  ./internal/catalog/ ./internal/mvcc/
```

PASS.

## Next blocker

E2E re-run (`GOOPG_RUN_BLOCKED_M0102_E2E=1
TestE2E_FailoverGoopgToPG/async`) will advance past the OID 2650 FATAL to
the next mapped-but-unseeded catalog. Probable candidates per the
empty-heap inventory in `bootstrapMappedLocalCatalogHeaps`: pg_am (2601)
and pg_amop (2602) already seeded; next likely is one of the other ~28
mapped catalogs whose pg_class row is still missing (pg_attrdef=2604,
pg_cast=2605, pg_conversion=2607, etc.) — but several of those *are*
already in `nailedLocalRels` (pg_attrdef, pg_constraint, pg_amop). The
empirical next FATAL must be captured to determine the actual blocker.

## Files touched

- `internal/initdb/initdb.go` (4 hunks: pgIndexInitialEntries entry +
  three placeholder OID lists)
- `internal/initdb/relcache_init.go` (1 hunk: nailedLocalRels idxSpec)
- `internal/initdb/pg_index_indkey_test.go` (1 hunk: pin map extend)
- `internal/initdb/btree_index_bootstrap_test.go` (1 hunk: mustHave extend)
- `internal/initdb/pg_aggregate_fnoid_index_test.go` (new)
- `docs/design/0106-0010-step3x-pg-aggregate-fnoid-index.md` (new)
- `docs/design/README.md` (index entry)
