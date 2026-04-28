# 0009 — B-tree Index Access Method (v0)

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

Milestone 5 calls for a B-tree index access method. pgbench's
`pgbench_accounts.aid`/`pgbench_branches.bid` lookups can't run without
one — the workload is dominated by primary-key probes. The buffer
manager (0005), page format (0006), heap tuple layout (already
shipped), and WAL (0008) are in place; the B-tree is built directly
on top of those.

References into upstream:

- `postgres/src/backend/access/nbtree/nbtsearch.c` — `_bt_search`,
  `_bt_first`, `_bt_next`.
- `postgres/src/backend/access/nbtree/nbtinsert.c` — `_bt_doinsert`,
  `_bt_split`.
- `postgres/src/backend/access/nbtree/nbtpage.c` — page initialisation,
  `BTPageOpaqueData`.
- `postgres/src/include/access/nbtree.h` — `BTPageOpaqueData`,
  `BTreeMetaPageData`, `BT_LEAF`, `BT_ROOT`, `BT_DELETED` flag bits.

## Decision

### Scope of v0

The on-disk B-tree subset that pgbench needs:

1. **Single-column index** keyed on a fixed-width type. v0 supports
   `int4` only — the type pgbench keys are. Adding more types is a
   `Compare` function plus an encoded-key codec; the seam is small.
2. **Page format mirroring upstream**: the standard page header (24
   bytes) plus a B-tree-specific 16-byte `special` area at the end
   of every block carrying sibling pointers, btree-level, and flag
   bits.
3. **Items**: each item is a `(key, ItemPointer)` pair. Internal
   pages use the same shape: the key is the smallest key in the
   right child, the ItemPointer carries `(blockNum, 0)` of that
   child.
4. **Search**: descend from root, binary-search each page's items
   for the rightmost key ≤ search key, follow the child pointer.
   At a leaf, binary-search again for the target slot.
5. **Insert**: descend to the leaf, insert in sorted order. If the
   leaf has insufficient space, split it into two leaves and propagate
   one separator key up. Recursive split up to the root; root split
   creates a new root one level up.
6. **Forward range scan**: position via search, walk leaves
   left-to-right via the right-sibling pointer.

Out of scope for v0:

- Concurrent modification (Lehman/Yao locking, page deletion). v0
  serialises all writes through an exclusive mutex on the index;
  pgbench drives many readers but writes serialise per-row at the
  heap level too, so the contention story stays acceptable for the
  initial milestone.
- Page deletion / VACUUM merge. Inserts only grow the tree;
  delete-then-vacuum will surface in milestone-5's VACUUM work.
- Variable-width keys (`text`, `numeric`, `bytea`, multi-column).
- Suffix truncation, `INCLUDE` columns, deduplication.
- Backward scans, prefix-bounded scans.
- Crash recovery via WAL records. v0 uses the buffer manager's
  WAL-LSN ordering for page durability but does not log per-record
  changes; a redo of a torn index page rebuilds the page from heap
  scan during VACUUM (recorded as a follow-up).

### Page layout

Every B-tree page is `BlockSize = 8192` bytes:

| Bytes 0..23     | 24..pd_lower-1 | pd_lower..pd_upper-1 | pd_upper..pd_special-1 | pd_special..BlockSize-1 |
| --------------- | -------------- | -------------------- | ---------------------- | ----------------------- |
| `PageHeaderData`| ItemId array   | (free space)         | item bodies            | `BTPageOpaqueData`      |

`pd_special = BlockSize - SizeOfBTPageOpaque = 8176`. The 16-byte
opaque area holds:

```
btpo_prev    BlockNumber  // left sibling (InvalidBlockNumber for leftmost)
btpo_next    BlockNumber  // right sibling (InvalidBlockNumber for rightmost)
btpo_level   uint32       // 0 = leaf, increasing toward root
btpo_flags   uint16       // BT_LEAF | BT_ROOT | BT_DELETED
unused       uint16       // padding to 16 bytes
```

This mirrors `BTPageOpaqueData` in
`postgres/src/include/access/nbtree.h`. We don't yet carry the
`xact` field upstream uses for page-deletion bookkeeping; v0 has
no page deletion.

### Item layout

Each item is encoded into the page's tuple region as:

```
key_len        uint16       // bytes in the key payload (4 for int4)
ipoint_block   uint32       // block number of heap row (or child page for internal)
ipoint_offset  uint16       // line-pointer slot of heap row (or 0 for internal)
key_bytes      [key_len]byte
```

`(key_len, ipoint_block, ipoint_offset)` form a 8-byte fixed prefix,
so the on-page item size is `8 + key_len`. Items are referenced via
the standard 4-byte ItemId line pointers in the lower area.

### Metapage

Block 0 of every B-tree relation is the metapage. It holds a
fixed-format payload aligned with upstream's `BTMetaPageData`:

```
magic    uint32   // 0x053162 (BTREE_MAGIC)
version  uint32   // 1 for v0
root     BlockNumber
level    uint32   // height of the tree, 0 if root is a leaf
fastroot BlockNumber  // == root for v0; matters once Lehman/Yao adds split-link tracking
fastlevel uint32
```

The metapage is created on `BTreeCreate`; it is never split, never
deleted. The root pointer changes whenever the root splits.

### Operations

#### `BTreeCreate(rel) -> *BTree`

- Calls `pool.PinNew` for blocks 0 (meta) and 1 (root, also leaf).
- Writes the meta payload pointing root=1, level=0.
- Writes a leaf opaque area on block 1 with `BT_LEAF | BT_ROOT`.
- MarksDirty on both pages.

#### `BTree.Insert(key, ItemPointer) -> error`

- Acquires the index's exclusive mutex.
- `descend(key)` returns the path of pinned pages from root to leaf.
- Inserts into the leaf if it fits.
- Otherwise splits: pick a midpoint, copy the right half to a freshly
  extended block, fix up sibling pointers, propagate the separator
  key (smallest key in right page) up the path. Repeat until the
  insert fits or we're at the root; root split allocates a new
  internal page and updates the metapage.
- Unpins/MarkDirty as it unwinds.

#### `BTree.Search(key) -> (ItemPointer, found bool, error)`

- Descend, binary-search the leaf, return the matching ItemPointer
  if any.

#### `BTree.RangeScan(low, high, fn)`

- Descend to the leaf containing `low`, walk right-sibling pointers
  emitting items in `[low, high]` until `high` is exceeded or
  `btpo_next` is `InvalidBlockNumber`.

### Concurrency

- One `sync.Mutex` per `*BTree`. All public methods acquire it.
- Pages are pinned/unpinned through the buffer pool the same way the
  heap path does. Pinned pages are read/written under the slot's
  `contentMu` (held implicitly while we're inside the index method).
- Two goroutines hitting different B-trees don't interfere; two
  goroutines on the same B-tree serialise.

This is intentionally simpler than upstream's Lehman/Yao right-link
crab-walk. pgbench is dominated by point lookups; the v0 mutex makes
the algorithm review tractable. The next iteration replaces the
mutex with proper buffer-manager exclusive/shared content locks plus
right-link traversal in search.

### Crash recovery

v0 does not emit per-record WAL for index inserts. Instead the WAL
layer's page-LSN ordering (already shipped) ensures that data files
contain only pages whose LSN is durable. Crash recovery for an index
is handled by:

1. The page-LSN check on flush (already implemented).
2. A `REINDEX`-style rebuild path that scans the heap and re-inserts
   every live tuple. For pgbench, the indexes are small enough that
   this works as a fallback. A real `xl_btree_*` WAL record set is
   queued for the milestone-5 follow-up that makes pg_dump/restore
   round-trip safely.

### What this doc does NOT cover

- Backward scans, multi-column keys, variable-length keys, NULL
  handling. Those grow naturally from `Compare`; deferred.
- VACUUM-driven page deletion / merge. Tracked under the VACUUM
  task in `.ralph/fix_plan.md`.
- WAL records for inserts/splits. Tracked separately; v0 reindex
  on recovery is the bridge.

## Alternatives Considered

- **Skip B-tree, use a hash index for pgbench.** Rejected: pgbench
  uses primary keys with range-friendly types; B-tree is the
  expected access method, and many catalog probes from `psql \d`
  iterate sorted btree results. Hash adds a code path with no
  net gain.
- **Use a third-party Go B-tree (`google/btree` or similar).**
  Rejected: their pages live in Go heap, not in our buffer pool, so
  durability and crash recovery break. The whole point of
  building an AM is that it lives in the page-aligned arena.
- **In-memory `map[int32]ItemPointer` until VACUUM is in.**
  Rejected: an index that doesn't survive a restart isn't useful
  for pgbench, and the persistence cost is the v0 layout above.

## Consequences

- pgbench's primary-key lookups can run on real on-disk indexes.
- VACUUM and `REINDEX` work picks up where v0 left off (page
  deletion + WAL records).
- The B-tree page layout is upstream-shaped, so `pg_filedump
  -i` would parse it modulo unimplemented header bits — useful as
  an early diagnostic when something looks wrong.
