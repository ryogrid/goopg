# M0106-0010 Step 3o — Populate `pg_attribute_relid_attnum_index` with composite-key btree tuples

**Status:** accepted (2026-05-17)
**Milestone:** [M0106 — PG Relcache Init File Compatibility](../../milestones/0106-pg-relcache-init-file-compat.md)
**Predecessors:** Steps 3a–3n of M0106-0010.

## Problem

After Step 3m landed a populated `pg_class_oid_index` (OID 2662) and
Step 3n corrected four `pgIndexInitialEntries.indkey` heap-attnum entries
(2659/2693/2701/3593), vanilla PG's standby boot advances past the
`column is not in index` FATAL emitted by every backend's
`systable_beginscan()` for the affected indexes.

`TestE2E_FailoverGoopgToPG/async` (run with
`GOOPG_RUN_BLOCKED_M0102_E2E=1`) now reaches a new blocker on the
first relation lookup that flows through `RelationBuildTupleDesc`:

```
FATAL: pg_attribute catalog is missing 1 attribute(s) for relation OID 2671
```

— from `relcache.c:498` (`elog(ERROR, "catalog is missing %d attribute(s) for relid %u")`).

### Root cause

`RelationBuildTupleDesc(relation)`
(`postgres/src/backend/utils/cache/relcache.c:436–500`) opens a
`systable_beginscan` against **`AttributeRelidNumIndexId = 2659`** with
scan keys `attrelid = X AND attnum > 0`, then iterates the matching
pg_attribute tuples. The scan decides between an index probe and a
sequential heap scan based on `IndexScanOK(rel, snapshot)`
(`relcache.c:381`): when `criticalRelcachesBuilt == true` (set right
after the seven local critical relcache entries finish loading), the
function returns true and `systable_beginscan` uses the index path.

`pg_attribute_relid_attnum_index` (OID 2659) is itself one of the seven
local critical indexes — `load_critical_index` succeeds because Steps
3a-3n satisfied every pg_class / pg_index / opclass / amop / amproc
prerequisite for *opening* the index. But opening only reads the metapage
(Step 3k). The empty `btm_root = P_NONE` placeholder produced by Step 3k
returns zero rows for every `(attrelid, attnum)` probe, so
`RelationBuildTupleDesc` increments its `need` counter by `relnatts` and
never decrements it. The `if (need > 0) elog(ERROR, …)` check at the end
of the function then FATALs with the count of expected-but-not-found
columns.

The first relation that triggers this is OID 2671 = `pg_database_datname_index`,
because it is the first **shared** critical index that
`RelationCacheInitializePhase3` loads after the local critical phase
flips `criticalRelcachesBuilt = true`. Local critical indexes themselves
never trigger the FATAL because they finish loading *before* the flip,
so their `RelationBuildTupleDesc` invocations use the sequential heap
scan fallback (which reads the pg_attribute rows already seeded by
`bootstrapPgAttributeTuples`).

## Fix

Mirror the approach taken in Step 3l (`pg_opclass_oid_index`) and Step 3m
(`pg_class_oid_index`): replace the Step 3k empty placeholder with a
2-block btree file (metapage + populated leaf-root) that carries one
IndexTuple per pg_attribute heap row, keyed on the index's actual
columns `(attrelid, attnum)`.

The new key type is a **composite (oid, int2)** — the first composite
key goopg seeds — so this step adds the corresponding tuple builder
alongside the existing single-column oid builder.

### Layout — `pgBuildIndexTupleOidInt2Key`

Mirrors PG `index_form_tuple` byte-for-byte for a no-nulls, no-varlena,
2-attribute tuple with `oid_ops` (`att1.typalign='i'`) and `int2_ops`
(`att2.typalign='s'`):

| Byte range | Field                  | Encoding                       |
|------------|------------------------|--------------------------------|
| 0..3       | `ItemPointerData.ip_blkid` | LE uint32 (heap block)     |
| 4..5       | `ItemPointerData.ip_posid` | LE uint16 (heap offset)    |
| 6..7       | `t_info`               | size in low 13 bits, no flags  |
| 8..11      | `attrelid`             | LE uint32 (oid_ops)            |
| 12..13     | `attnum`               | LE int16 (int2_ops)            |
| 14..15     | MAXALIGN padding       | zero                           |

Total size = `MAXALIGN(IndexTupleHeader + att1.len + att2.len) =
MAXALIGN(8 + 4 + 2) = 16` bytes. `t_info`'s low 13 bits store the
MAXALIGN'd total (16) so PG's `IndexTupleSize` matches `len(out)`.

### Heap TID tracking

The composite key tuple's `ItemPointerData` must point at the actual
heap row each (attrelid, attnum) pair lives at — not a synthesised
TID. Step 3l/3m had simple cases:

- Step 3l: all 12 pg_opclass rows pack onto block 0, so TIDs are
  `(0, i+1)` derivable from insertion order.
- Step 3m: pg_class heap spans multiple pages already, and
  `bootstrapPgClassTuples` (built on `writeMultiPageHeap`) returned a
  `map[uint32]heapTID` keyed on OID.

Step 3o uses `writeMultiPageHeapRows` (the pre-built-rows variant),
which did not return TIDs. The minimal-scope fix:

1. Widen `writeMultiPageHeapRows` to return `([]heapTID, error)` — the
   per-row TIDs in input order. Six existing callers (`bootstrapPgAm*`,
   `bootstrapPgProc*`, `bootstrapPgOpclass*`, `bootstrapPgAmop*`,
   `bootstrapPgAmproc*`, `bootstrapPgIndex*`) discard the slice; only
   pg_attribute consumes it.
2. Refactor `bootstrapPgAttributeTuples` to return
   `map[pgAttrTIDKey]heapTID` where `pgAttrTIDKey = {AttRelID, AttNum}`.
3. Add `bootstrapPgAttributeRelidAttnumIndex(dataDir, tids)` that filters
   to `attnum > 0`, sorts lexicographically by `(attrelid, attnum)`, and
   writes a 2-block file to `base/{1,5}/2659` and `global/2659`.

Sort order is critical: btree leaves require monotonic key ordering
across line pointers (PG `_bt_binsrch`). The composite comparator is
lexicographic over (attrelid, attnum), both ascending — matching the
opclass tuple in `pgIndexInitialEntries`:

```go
entry(2659, 1249, []int16{1, 5},
      []uint32{oidOps, int2Ops},
      []uint32{0, 0}, true, true)
```

### Attnum > 0 filter

PG's `RelationBuildTupleDesc` only probes `attnum > 0` — system
attributes (attnum ∈ {-1..-7} for ctid/xmin/cmin/xmax/cmax/tableoid)
are resolved from a hardcoded table (`SystemAttributeDefinition`),
not via pg_attribute. The bootstrap pg_attribute heap currently seeds
only user/catalog columns anyway, so the filter has no effect today
but documents the index contract for future seeding work.

### Page sizing

The current nailed-rel set has 30 relations (5 shared + 5 shared
indexes + 13 local heaps + 17 local indexes). Attribute counts:
pg_class=33, pg_attribute=24, pg_proc=30, pg_index=21, pg_database=16,
pg_authid=12, pg_subscription=9, pg_opclass=9, pg_amop=9, pg_amproc=6,
pg_auth_members=5, pg_shseclabel=6, pg_am=4, plus 30 indexes × 1-4 key
columns ≈ 60. Total ≈ 240 entries.

Per-tuple cost in the leaf page: 16 bytes (IndexTuple) + 4 bytes
(ItemId) = 20 bytes. 240 × 20 = 4800 bytes; with the 24-byte page
header and 16-byte BTPageOpaqueData, the page consumes ≈ 4840 bytes of
8192 available. Single leaf-root page suffices.

If the bootstrap nailed-rel set grows past ~400 attribute entries, the
single-page assumption breaks and the leaf-root must split into an
inner + leaf level. That refactor is deferred until concrete pressure
appears.

## Files touched

- `internal/initdb/initdb.go`
  - `writeMultiPageHeapRows` widened to `([]heapTID, error)`.
  - `bootstrapPgAttributeTuples` returns
    `map[pgAttrTIDKey]heapTID`.
  - New `pgAttrTIDKey` struct.
  - Six call sites updated to discard the TID slice with `_, err :=`.
  - `Init` consumes `pgAttrTIDs` and calls
    `bootstrapPgAttributeRelidAttnumIndex` after
    `bootstrapPgClassOidIndex`.
- `internal/initdb/btree_index_bootstrap.go`
  - New `pgBuildIndexTupleOidInt2Key(heapBlk, heapOff, attrelid, attnum)`.
  - New `bootstrapPgAttributeRelidAttnumIndex(dataDir, tids)`.
- `internal/initdb/btree_index_bootstrap_test.go`
  - `TestPgBuildIndexTupleOidInt2KeyLayoutMatchesPG18` pins the 16-byte
    byte-exact layout.
  - `TestBootstrapPgAttributeRelidAttnumIndexWritesPopulatedBtree`
    end-to-ends the 2-block output at all three on-disk locations and
    verifies (key, TID) round-trip against the pg_attribute heap map.

## Verification

- `go test -count=1 -run
  'TestPgBuildIndexTupleOidInt2KeyLayoutMatchesPG18|TestBootstrapPgAttributeRelidAttnumIndex'
  ./internal/initdb/` — PASS.
- `go test -count=1 -run
  'TestPgBuildIndexTupleOidKey|TestPgBuildBtreeLeafRoot|TestPgBuildBtreeMetapage|TestBootstrapPgOpclassOidIndex|TestBootstrapPgClassOidIndex|TestPgIndex|TestBootstrapPgIndex|TestPgAm|TestPgOpclass|TestPgProc|TestNailedIndexRelnatts|TestPgClassOidIndexHasSingleKey|TestMakeBtreeRootPage'
  ./internal/initdb/` — every Step 3a–3n pin still PASS.
- `go test -count=1 ./internal/initdb/` — 14 pre-existing baseline
  failures (`TestMigration*`, `TestCreate*`, `TestBootstrappedPG*`,
  `TestSynchronousCommitFlushesByDefault`, `TestOpenOldClusterWithoutM0030*`,
  `TestSystemCatalogRelfilesAreValidHeapPages`,
  `TestCommittedTableSurvivesCrashRestart`,
  `TestRuntimeCloseTriggersFinalCheckpoint`,
  `TestMultipleTablesLoadFromHeap`) match the documented Step 3n baseline
  byte-for-byte; no new regressions.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — all PASS.

## Next blocker (Step 3p)

Once the standby's `RelationBuildTupleDesc` resolves pg_attribute
rows for the six shared critical indexes, the next probable FATAL
shifts to one of:

- `pg_authid` row lookup via `pg_authid_oid_index` (2677) — the
  shared-catalog btree placeholder is still empty (Step 3k).
- `pg_database` row lookup via `pg_database_oid_index` (2672) — ditto.

Either surfaces once goopg's standby tries to authenticate the
incoming PG replica connection. Both are single-column oid-keyed
indexes that fall out of the existing `pgBuildIndexTupleOidKey` +
`bootstrapPgClassOidIndex` template — the next loop should populate
them in one step.
