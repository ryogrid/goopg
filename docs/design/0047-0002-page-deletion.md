# B-tree Page Deletion (Index Vacuum) — M0047-0002

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

After `DELETE` statements the B-tree index retains entries pointing to dead
heap tuples. These stale entries waste space, slow scans, and (without
removal) accumulate unboundedly. PostgreSQL's VACUUM performs an index cleanup
pass after the heap pass to remove dead index entries and optionally delete
empty index pages.

## 2. Design

### 2.1 Dead TID collection (`internal/vacuum/vacuum.go`)

`vacuumCore` now populates `Stats.DeadTIDs []storage.ItemPointer` with the
heap `(block, offset)` pairs for every tuple it reclaims. These are the TIDs
passed to `VacuumIndexPages` after the heap pass.

### 2.2 B-tree index vacuum (`internal/access/btree/btree_vacuum.go`)

**`BTree.VacuumIndexPages(deadTIDs []storage.ItemPointer) (int, error)`**:

1. Builds an O(1) `map[uint64]bool` from `deadTIDs`.
2. Walks all leaf pages left-to-right via the `Next` chain (starting from
   `findLeftmostLeaf`).
3. For each leaf, saves the first key (used later for parent descent), then
   rewrites the page without dead items.
4. Empty leaves are marked `BTDeleted` and added to a post-process list.
5. After the scan, calls `unlinkEmptyLeaf` for each empty leaf:
   - Updates the left sibling's `Next` pointer.
   - Updates the right sibling's `Prev` pointer.
   - Re-descends with the saved first key to locate the parent, then removes
     the downlink via `removeDownlinkFromParent`.
6. If the tree is now empty (`isTreeEmpty`), calls `resetToEmptyRoot`.

**`resetToEmptyRoot`**: Reinitialises block 1 as a fresh empty `BTLeaf|BTRoot`
page and updates the metapage to point to it. Old blocks become unreachable
orphans but do not affect correctness — the root is now fresh and `Insert`/
`RangeScan` work correctly.

**Leftmost-child key convention**: The B-tree's first item on each internal
page has a nil key pointing to the leftmost child. When a leaf is the leftmost
child (nil key), `removeParentDownlinkByBlock` walks the tree to find the
parent by block number rather than by key. When the removed item was the
leftmost, the new first item's key is cleared to nil to preserve the invariant.

### 2.3 VACUUM SQL integration (`internal/executor/operators_vacuum.go`)

`vacuumIndexes(ctx, tbl, deadTIDs)` is called from `vacuumOp.Next()` after
each successful heap vacuum. For each `btree` index on the table it calls
`btree.Open` + `tree.VacuumIndexPages(deadTIDs)`.

The autovacuum launcher also calls `VacuumWithOptions` which now collects
`DeadTIDs` in `Stats`, but autovacuum does not yet call index vacuum
(left as a follow-up; manual VACUUM SQL is the primary path).

## 3. Limitations (v0)

- Only leaf pages are deleted; partially-empty internal pages from partial
  leaf deletions are left in place (ghost internal pages). Correctness is
  maintained because the metapage + fresh root fully bypasses orphaned pages.
- Empty internal pages that result from partial leaf deletion are not
  recursively removed in this release.
- WAL records for page deletion (`XLOG_BTREE_MARK_PAGE_HALFDEAD` /
  `XLOG_BTREE_UNLINK_PAGE`) are not emitted; pages are flushed with FPI.
- Autovacuum does not yet trigger index vacuum automatically.

## 4. Tests (`internal/access/btree/btree_vacuum_test.go`)

| Test | Coverage |
|---|---|
| `TestVacuumIndexPagesNoDeadTIDs` | Empty dead set is a no-op |
| `TestVacuumIndexPagesPartial` | 200-entry tree, half deleted — correct survivors |
| `TestVacuumIndexPagesAllDeleted` | **DoD**: all 500 entries deleted → tree empty → Insert works |
| `TestVacuumIndexPagesSingleLeaf` | Single-page tree with 3 entries, 1 deleted |
| `TestVacuumIndexPagesLargeTree` | 2000-entry multi-level tree, half deleted — correct survivors |
