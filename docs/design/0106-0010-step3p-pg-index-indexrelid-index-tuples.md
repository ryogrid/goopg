# M0106-0010 Step 3p — Populate `pg_index_indexrelid_index` (OID 2678)

Status: PARTIAL (btree populated; downstream FATAL persists pending Step 3q)

## Motivation

After Step 3o landed (populated `pg_attribute_relid_attnum_index` so
`RelationBuildTupleDesc` finds column tuples via index when
`criticalRelcachesBuilt = true`), the next PG-standby boot FATAL is:

```
FATAL:  cache lookup failed for index 2671
```

The error originates at `postgres/src/backend/utils/cache/relcache.c:1467-1471`
inside `RelationInitIndexAccessInfo`:

```c
tuple = SearchSysCache1(INDEXRELID,
                        ObjectIdGetDatum(RelationGetRelid(relation)));
if (!HeapTupleIsValid(tuple))
    elog(ERROR, "cache lookup failed for index %u",
         RelationGetRelid(relation));
```

The `SearchSysCache1(INDEXRELID, …)` syscache uses
`pg_index_indexrelid_index` (OID 2678) when `criticalRelcachesBuilt` is
true — i.e. for every shared critical index loaded after the LOCAL
phase. Step 3k seeded an empty btree placeholder at `base/{1,5}/2678`
+ `global/2678`, so the index returned zero rows for every probe.

PG's shared-critical-index pass (`RelationCacheInitializePhase3`,
relcache.c:4213-4223) starts with `load_critical_index(DatabaseNameIndexId,
DatabaseRelationId) = load_critical_index(2671, 1262)`, hits the empty
btree, FATALs with the message above on the first iteration.

## Approach

Mirror Step 3l (`pg_opclass_oid_index`) and Step 3m
(`pg_class_oid_index`) — both single-column oid-keyed btrees over per-DB
heap rows, using the same `pgBuildIndexTupleOidKey` 16-byte
`oid_ops` IndexTuple layout and the same `pgBuildBtreeLeafRootPage` +
`pgBuildBtreeMetapageWithRoot` page builders.

### Heap-TID propagation

`bootstrapPgIndexTuples` (Step 3g) now returns
`(map[uint32]heapTID, error)` instead of plain `error`, keyed by
`indexrelid`. The map carries the per-row `(block, offset)` location
where `writeMultiPageHeapRows` placed each `Form_pg_index` row in
`base/{1,5}/2610`. The Step 3p builder consumes the map to stamp each
leaf IndexTuple's `t_tid` at the matching heap position.

Existing callers that discard the return (`pg_index_bootstrap_test.go`'s
heap-only test) drop it explicitly via `_, err := …`.

### Leaf-root layout

23 entries (6 shared + 17 local, matching `pgIndexInitialEntries()`)
all fit on a single 8 KiB page:

- file = 2 blocks (metapage + leaf-root)
- metapage `btm_root = 1`, `btm_level = 0`
- leaf-root flags = `BTP_LEAF | BTP_ROOT` (0x03)
- leaf items sorted ascending by `indexrelid` so PG's `_bt_binsrch`
  works via the standard ordered search

Each IndexTuple is 16 bytes: 8-byte `IndexTupleHeader` (4-byte heapBlk
LE + 2-byte heapOff LE + 2-byte t_info=16) + 4-byte oid key
(LE uint32, `oid_ops` compares unsigned) + 4-byte MAXALIGN pad.

### Write paths

The btree is written to all three locations so PG opens the right copy
regardless of `dbNode` resolution:

- `base/1/2678`
- `base/5/2678`
- `global/2678` — fallback for `formrdesc`-style `InvalidOid` dbNode
  lookups on nailed relations.

This matches the existing Step 3l / 3m / 3o pattern.

## Files changed

- `internal/initdb/btree_index_bootstrap.go` — new
  `bootstrapPgIndexIndexrelidIndex(dataDir, tids)` function appended
  after Step 3o's `bootstrapPgAttributeRelidAttnumIndex`.
- `internal/initdb/initdb.go` — `bootstrapPgIndexTuples` signature
  bumped to `(map[uint32]heapTID, error)`; `Init` captures the map
  via `pgIndexTIDs, err :=` and invokes
  `bootstrapPgIndexIndexrelidIndex(abs, pgIndexTIDs)` immediately after
  the heap is seeded.
- `internal/initdb/pg_index_bootstrap_test.go` — call updated to
  `_, err := bootstrapPgIndexTuples(dir)` (discards the new TID map).
- `internal/initdb/btree_index_bootstrap_test.go` — new
  `TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree` pins
  the 2-block file shape (metapage `btm_root == 1`), 23-item leaf,
  oid-sorted ascending, per-OID TID round-trip against the heap map,
  and presence of every shared-critical-index OID (2671/2/6/7/95, 3593)
  plus the 17 local nailed-index OIDs.

## Verification

- `go test -count=1 -run TestBootstrapPgIndexIndexrelidIndexWritesPopulatedBtree
  ./internal/initdb/` — PASS.
- Cross-package smoke: `go test -count=1 ./internal/executor/
  ./internal/server/ ./internal/storage/ ./internal/catalog/
  ./internal/mvcc/` — PASS.
- `go test -count=1 ./internal/initdb/` — same 14 pre-existing baseline
  failures as Step 3o (`TestMigration*`, `TestCreate*`,
  `TestBootstrappedPG*`, `TestSynchronousCommitFlushesByDefault`,
  `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) — no new regressions.
- E2E run (`GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -run
  TestE2E_FailoverGoopgToPG/async`) confirmed via filesystem
  inspection that the 16 KiB btree file is correctly present at all
  three on-disk locations on both the goopg primary AND the PG
  standby after pg_basebackup. The exact bytes match
  the standalone `goopg init` output (`cmp` — MATCH).

## Status — PARTIAL

The standby still FATALs with `cache lookup failed for index 2671`
after Step 3p lands. Investigation revealed a deeper catalog-state
inconsistency that is OUT OF SCOPE for Step 3p and tracked as the
next Step 3q blocker:

- `pgIndexInitialEntries()` (initdb.go) and `nailedLocalRels`
  (relcache_init.go) BOTH OMIT OID 2678 (`pg_index_indexrelid_index`)
  itself. Consequently:
  - `pg_class` heap has no row for OID 2678
    (verified by direct heap dump — only 25 rows for the seeded
    relations; 2678 not among them).
  - `pg_index` heap has no row for `indexrelid = 2678`
    (verified — 23 rows, none for 2678).
- PG's LOCAL critical-index pass calls
  `load_critical_index(IndexRelidIndexId=2678, IndexRelationId=2610)`
  early in `RelationCacheInitializePhase3` (relcache.c:4183). With no
  pg_class row, `ScanPgRelation(2678) → systable_getnext()` should
  return NULL, then `RelationBuildDesc(2678) → NULL`, then
  `load_critical_index` should `PANIC: could not open critical system
  index 2678`. The PG log shows no such PANIC, suggesting PG is
  silently falling through some path that leaves the relcache entry
  for 2678 partial — which then defeats my Step 3p btree on the
  subsequent SHARED pass (where 2671 is the first probe).
- Step 3p's btree IS load-bearing for the eventual fix — even after
  Step 3q closes the 2678 inconsistency, the SHARED critical-index
  pass still needs a populated `pg_index_indexrelid_index` to satisfy
  `SearchSysCache1(INDEXRELID, 2671..3593)`.

## Next blocker (Step 3q)

Add OID 2678 to `pgIndexInitialEntries()` + `nailedLocalRels` (with
`indrelid=2610`, `indkey={1}` (indexrelid attnum), `indclass={oid_ops}`,
`indcollation={0}`, `unique=true`, `primary=true`). Also add 2678 to
the empty-placeholder list at `initdb.go:670` so the file exists for
PG's mdopen before Step 3p overwrites it.

After Step 3q, the heap TID for 2678's Form_pg_index row will be
included in the map returned by `bootstrapPgIndexTuples`, so Step 3p
will automatically pick it up — no change to Step 3p code is required.
