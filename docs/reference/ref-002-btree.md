# REF-002: B-Tree Index

## Overview

The B-Tree index provides ordered key→ItemPointer lookups for
primary keys, unique indexes, and non-unique indexes. goopg's
implementation is a Lehman-Yao B-link-tree with high-key-based
right-link recovery, matching the PostgreSQL approach.

## goopg Implementation

**Package:** `internal/access/btree/`

### Key Types

- `BTree` — one per index relation. Holds a reference to the storage
  `Pool` for page I/O.
- `BTreeMeta` — the metapage (root block number, level count,
  fast-root for single-page optimisation).
- `item` — an on-page entry containing `key []byte` and
  `ptr ItemPointer` (block + slot in the heap).
- `BTreeOpaque` — per-page header (level, isLeaf, high key,
  right-sibling link).

### Data Flow (Search)

```
BTree.Search(key)
  ├─ readMeta() — get root block
  └─ descendToLeaf(key)
       ├─ pinR(root), readOpaque, findChildBlock → next block
       ├─ repeat until leaf
       └─ linear scan of leaf items for matching key
```

### Page Structure

Each B-tree page:
```
[PageHeader][BTreeOpaque][item_1][item_2]...[item_N][free space]
```

Items are stored in sorted order. The opaque header contains the
level, leaf flag, high key (rightmost key of the left-sibling
after a split), and next-block pointer for right-link recovery.

### Splits

When `insertIntoBlock` finds no space on the leaf, it:
1. Pins a freshly-extended block as the right sibling.
2. Redistributes items (half stay, half go right).
3. Stamps high keys and sets `op.Next` links.
4. Walks up the parent path to insert a separator key.

The global `splitMu` serialises concurrent splits on the same
tree. Non-split inserts on different leaves run without the lock.

### Insert (non-split)

```
BTree.Insert(key, ptr)
  ├─ tryInsertNoSplit → descendToLeaf, pinW(leaf),
  │    insertItemSorted, MarkDirty (or MarkDirtyChangeRecord)
  └─ on overflow → splitMu, retry via insertIntoBlock
```

## PostgreSQL Implementation

PostgreSQL's B-tree (`nbtree`) is a Lehman-Yao B-link-tree with
several additional features:

- **Dedicated sort for index build** — `_bt_spool` + `_bt_leafbuild`
  (sort then bulk-load) instead of row-by-row insertion.
- **Page deletion** — pages are not just split but also merged
  when they become empty (`_bt_pagedel`).
- **Dedup** — PostgreSQL 13+ optionally deduplicates duplicate keys.
- **Incomplete splits** — if a split crashes partway, the
  right-link chain allows recovery.
- **Fast root optimisation** — if the entire tree fits on one page,
  PostgreSQL keeps it as a "root-only" tree with no internal pages.
  goopg has a `FastRoot`/`FastLevel` but does not currently
  exploit it for single-page trees.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Insert order | Any order | Bulk-load for initial build |
| Page deletion | None (VACUUM may reclaim later) | `_bt_pagedel` on empty pages |
| Split serialisation | `splitMu` mutex | Lock coupling with right-link recovery |
| High key semantics | Copied from original page | Strictly less than right page's minimum |
| Key encoding | Binary `encodeProbeKey` | `_bt_compare` with type-specific callbacks |

## Potential Optimisations or Corrections

- **Bulk-load on initial index creation** would dramatically speed
  up `pgbench -i` (currently 30+s for scale-3 primary keys).
- **Page deletion** would let DROP/VACUUM reclaim space.
- **Lock-free split** (lock coupling) would remove `splitMu`
  contention under heavy concurrent INSERT workloads.

## References

- goopg: `internal/access/btree/btree.go`
- PG B-tree overview: `postgres/src/backend/access/nbtree/README`
- PG insert: `postgres/src/backend/access/nbtree/nbtinsert.c`
- PG search: `postgres/src/backend/access/nbtree/nbtsearch.c`
- PG sort/build: `postgres/src/backend/access/nbtree/nbtsort.c`
