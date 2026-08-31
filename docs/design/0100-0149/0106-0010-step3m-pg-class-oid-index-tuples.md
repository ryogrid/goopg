# M0106-0010 Step 3m — Populate `pg_class_oid_index` with btree index tuples

**Status:** accepted (2026-05-17)
**Milestone:** [M0106 — PG Relcache Init File Compatibility](../../milestones/0106-pg-relcache-init-file-compat.md)
**Predecessors:** Steps 3a–3l of M0106-0010.

## Problem

After Step 3l landed a populated `pg_opclass_oid_index` (OID 2687),
vanilla PG's standby boot advances past `LookupOpclassInfo(name_ops)`
and successfully loads all seven **local** critical indexes (`pg_class_oid_index`,
`pg_attribute_relid_attnum_index`, `pg_index_indexrelid_index`,
`pg_opclass_oid_index`, `pg_amproc_fam_proc_index`,
`pg_rewrite_rel_rulename_index`, `pg_trigger_tgrelid_tgname_index`).

At this point `RelationCacheInitializePhase3`
(`postgres/src/backend/utils/cache/relcache.c:4196`) flips
`criticalRelcachesBuilt = true` and immediately proceeds to load the six
**shared** critical indexes (`pg_database_datname_index`,
`pg_database_oid_index`, `pg_authid_rolname_index`,
`pg_authid_oid_index`, `pg_auth_members_member_role_index`,
`pg_shseclabel_object_index`). The very first one,
`DatabaseNameIndexId = 2671`, PANICs:

```
PANIC: could not open critical system index 2671
```

from `load_critical_index` (`relcache.c:4408`) — `RelationBuildDesc(2671)`
returns NULL.

### Root cause

`RelationBuildDesc` calls `ScanPgRelation(2671, indexOK=true)`. Now that
`criticalRelcachesBuilt == true`, `ScanPgRelation` switches from its
no-critical-indexes-yet sequential-scan fallback (relcache.c:383) to an
index lookup against **`pg_class_oid_index`** (OID **2662**). The empty
btree placeholder produced by Step 3k (`btm_root = P_NONE`) returns zero
rows for *every* pg_class lookup, so `ScanPgRelation` returns NULL,
`RelationBuildDesc` returns NULL, and `load_critical_index` PANICs.

Step 3l unblocked the **local** indexes because the seven local
`load_critical_index` calls happen *before* `criticalRelcachesBuilt` flips
true — so they all still use the seq-scan fallback. Step 3m must close
the index-lookup path for any pg_class scan that runs after the flip.

## Scope (this step)

Populate the on-disk btree file for `pg_class_oid_index` (OID 2662) with
one IndexTuple per nailed-relation pg_class heap row. All other nailed
indexes remain Step-3k empty btrees; the next E2E rerun will surface the
next concrete blocker. (Confirmed empirically: after Step 3m landed, the
test advances past PANIC 2671 to a separate `FATAL: column is not in
index` from `RelationInitIndexAccessInfo` — Step 3n territory.)

## Design

### Heap-row TID tracking

`writeMultiPageHeap` (used by `bootstrapPgClassTuples`) packs nailed rels
across however many 8 KiB pages are required. The btree IndexTuples must
carry the *actual* (block, offset) each row landed at, so the function
signature is widened to:

```go
func writeMultiPageHeap(dataDir, relFile string,
    cols []catalog.Column, rels []nailedRel,
    rowFn func(nailedRel) executor.Row,
) ([]heapTID, error)
```

with a new local type:

```go
type heapTID struct {
    Block  uint32
    Offset uint16   // 1-based, matches PG's OffsetNumber convention
}
```

`bootstrapPgClassTuples` now returns `map[oid]heapTID` so the caller can
look up each nailed rel's TID by OID without depending on slice order.

### IndexTuple + page layout

Reuses Step 3l's builders verbatim:

* `pgBuildIndexTupleOidKey(blk, off, oid)` — 8-byte IndexTupleHeader
  (ip_blkid, ip_posid, t_info=size=16) + 4-byte LE oid key + 4-byte
  MAXALIGN pad = 16 bytes total. PG's `_bt_compare` calls `oidcmp` on
  the [8..11] window.
* `pgBuildBtreeLeafRootPage(sortedTuples)` — `BTP_LEAF|BTP_ROOT` page with
  line pointers from byte 24 growing forward, tuples from
  `BlockSize-16` growing backward.
* `pgBuildBtreeMetapageWithRoot(rootBlk=1, level=0)` — `btm_root=1`,
  `btm_fastroot=1`, `BTREE_MAGIC`, `BTREE_VERSION=4`,
  `btm_last_cleanup_num_heap_tuples=-1.0`, opaque flag `BTP_META`.

### Bootstrap entry point

```go
func bootstrapPgClassOidIndex(dataDir string, tids map[uint32]heapTID) error
```

* iterates the map, sorts the (oid, block, offset) triples ascending by
  OID (required by PG's `_bt_binsrch`),
* builds N×16-byte index tuples,
* assembles a 2-block file (metapage→leaf-root),
* writes to `base/1/2662`, `base/5/2662`, and `global/2662`. The
  triple-write pattern matches Step 3l and Step 3k — PG's `formrdesc`
  may use `InvalidOid` for the dbNode of a nailed index, in which case
  the relfile is opened under `global/`; the per-database copies cover
  the cases where the dbNode resolves to template1 (OID 1) or postgres
  (OID 5).

### Init wiring

`Init` captures the returned TID map from `bootstrapPgClassTuples` and
threads it into `bootstrapPgClassOidIndex`, placed immediately after the
existing `bootstrapPgOpclassOidIndex` call so both index files are
populated before `bootstrapCLog` / `bootstrapRelcacheInitFiles` run.

## Why this scope and not more

The per-loop pattern established by Steps 3a–3l: rerun
`TestE2E_FailoverGoopgToPG/async`, capture the next FATAL, land the
narrowest fix that clears it. Populating *every* nailed index in one
loop would couple variable-width key encoders (cstring/name keys for
`pg_class_relname_nsp_index`, multi-column tuples for
`pg_attribute_relid_attnum_index`, etc.) and risk cascading regressions
that obscure which subsystem broke. Step 3m intentionally reuses
Step 3l's oid-keyed builder verbatim because `pg_class_oid_index`
shares the exact same key shape.

## Verification

* New test `TestBootstrapPgClassOidIndexWritesPopulatedBtree` calls
  `bootstrapPgClassTuples` + `bootstrapPgClassOidIndex` and reads back
  each of the three on-disk locations (`base/{1,5}/2662`, `global/2662`),
  asserting:
  * total file length = 2 × BlockSize,
  * metapage `btm_root == 1` (block 1 = the populated leaf-root),
  * leaf line-pointer count matches the number of nailed rels,
  * every IndexTuple's [8..11] OID window decodes ascending, and
  * every (`ip_blkid`, `ip_posid`) matches the heap-side `tids` map
    by OID — guarding against silently-corrupt TID-to-OID alignment.
* `go test -count=1 ./internal/initdb/`: 14 pre-existing baseline
  failures unchanged (stash-based baseline diff confirmed).
* `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/`: PASS.
* `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test
  -run TestE2E_FailoverGoopgToPG/async ./internal/testport/`:
  advances past PANIC 2671. Next blocker recorded as
  `FATAL: column is not in index` (Step 3n).

## Files

* `internal/initdb/initdb.go`
  * widen `writeMultiPageHeap` to return `[]heapTID`,
  * add `heapTID` struct,
  * widen `bootstrapPgClassTuples` to return `map[uint32]heapTID`,
  * thread map through `Init` into the new step-3m call.
* `internal/initdb/btree_index_bootstrap.go`
  * add `bootstrapPgClassOidIndex(dataDir, tids)`.
* `internal/initdb/btree_index_bootstrap_test.go`
  * add `TestBootstrapPgClassOidIndexWritesPopulatedBtree`.
