# M0106-0010 Step 3bk — pg_language_oid_index (OID 2682) catalog seed

## Context

E2E test `TestE2E_FailoverGoopgToPG/async` (gated by
`GOOPG_RUN_BLOCKED_M0102_E2E=1`) drives a real PG18 standby boot from
goopg's bootstrap output. After Step 3bj seeded
`pg_language_name_index` (OID 2681), the standby is expected to surface
the companion PKEY blocker:

```
FATAL: could not open relation with OID 2682
```

OID 2682 = `pg_language_oid_index` per
`postgres/src/include/catalog/pg_language.h:70`:

```c
DECLARE_UNIQUE_INDEX_PKEY(pg_language_oid_index, 2682,
    LanguageOidIndexId, pg_language, btree(oid oid_ops));
MAKE_SYSCACHE(LANGOID, pg_language_oid_index, 4);
```

This is the PRIMARY KEY companion to Step 3bj's UNIQUE-non-PKEY
`pg_language_name_index`. PG's `RelationCacheInitializePhase3` loads
this index as part of the relcache scan over pg_language during early
backend startup (specifically when constructing the LANGOID syscache),
so the lookup fires before any user query.

## Root cause

`RelationIdGetRelation(2682)` calls `RelationBuildDesc → ScanPgRelation`
which, after Step 3bj's flip of `criticalRelcachesBuilt`, dispatches
through `pg_class_oid_index`. There is no `Form_pg_class` row for OID
2682 → `ScanPgRelation` returns NULL → `relation_open` FATALs with
"could not open relation with OID 2682".

## Fix

Pure catalog-seed addition mirroring single-column `oid_ops` UNIQUE
PKEY pattern of Steps 3ax (pg_extension_oid_index 3080), 3at
(pg_event_trigger_oid_index 3468), 3bd (pg_foreign_data_wrapper_oid_index
112), 3bg (pg_foreign_server_oid_index 113), and 3l
(pg_opclass_oid_index 2687). No encoder, builder, or `Init` flow change.

### `internal/initdb/initdb.go`

`pgIndexInitialEntries` gains:

```go
entry(2682, 2612, []int16{1}, []uint32{oidOps}, []uint32{0}, true, true)
```

- `IndexRelid = 2682` — index OID.
- `IndRelid = 2612` — pg_language heap OID (already a nailed local rel).
- `IndKey = []int16{1}` — `oid` is attnum 1 in pg_language.
- `IndClass = []uint32{oidOps}` — single `oid_ops` opclass.
- `IndCollation = []uint32{0}` — `oid_ops` carries no collation.
- `IsUnique = true, IsPrimary = true` — `DECLARE_UNIQUE_INDEX_PKEY`.

The three placeholder OID lists in `bootstrapPostgresDatabase`
(`base/1/`, `base/5/`, `global/`) already include `2682` (bundled with
`2678, 2679, 2680, 2682` from an earlier sweep). No edit needed there.

### `internal/initdb/relcache_init.go`

`nailedLocalRels` idxSpec list gains `{2682, "pg_language_oid_index"}`
after the Step 3bj 2681 entry. `flattenRels` + `pgIndexNattsByOID()`
derives `RelKind='i', RelNatts=1`, satisfying PG's
`RelationInitIndexAccessInfo` `relnatts == indnatts` check
(`relcache.c:1492`).

## Flow

The seed threads automatically through the existing bootstrap flow:

1. `bootstrapPgClassTuples` writes a `Form_pg_class` row for OID 2682.
2. `bootstrapPgAttributeTuples` writes one pg_attribute row for the
   single `oid` key column.
3. `bootstrapPgIndexTuples` writes the `Form_pg_index` row and captures
   the heap TID in `pgIndexTIDs[2682]`.
4. `bootstrapPgIndexIndexrelidIndex` adds the leaf for 2682 to the
   populated 2-page btree at `base/{1,5}/2679 + global/2679`.
5. `bootstrapPgClassOidIndex` adds the corresponding leaf at file 2662.
6. `bootstrapPgAttributeRelidAttnumIndex` adds the composite-key leaf
   at file 2659.

The Step-3k empty-btree placeholder at `base/{1,5}/2682 + global/2682`
is sufficient because pg_language is currently unpopulated (no language
rows are bootstrapped) — any `SearchSysCache1(LANGOID, …)` probe
correctly returns no row.

## Regression pins

- `TestPgLanguageOidIndexSeededFromInitialEntries` — pins
  `(IndRelid=2612, IndKey=[1], IsUnique=true, IsPrimary=true,
  IndCollation=[0])`.
- `TestNailedLocalRelsContainsPgLanguageOidIndex` — pins
  `(RelName="pg_language_oid_index", RelKind='i', RelNatts=1)`.

Both in `internal/initdb/pg_language_oid_index_test.go`.

Existing pins extended:

- `TestPgIndexInitialEntriesIndkeyMatchesPG18` map gains `2682: {1}`
  (strict count guard auto-rejects future additions without map
  updates).
- `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree::mustHave`
  extended with 2682 so the populated 2679 btree must carry this leaf.

## Verification

- `go build ./...` — PASS.
- `go test -count=1 -run 'TestPgLanguageOidIndex|TestNailedLocalRels
  ContainsPgLanguageOidIndex|TestPgLanguageNameIndex|TestPgIndexInitial
  EntriesIndkeyMatchesPG18|TestBootstrapPgIndexIndexrelidIndex|TestNailed
  IndexRelnattsAgreesWithIndnatts|TestPgIndexColDefsMatchesRelcache
  Attrs|TestBootstrapPgIndexTuples|TestPgClassOidIndexHasSingleKey
  Column' ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3bj (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- Cross-package smoke `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.

Closes Step 3bj's deferred companion — both pg_language indexes (2681
name + 2682 oid) are now seeded.
