# REF-002: B-Tree Index

…(existing content up to "Key Differences" unchanged)…

## PostgreSQL Implementation (Deep Dive)

PostgreSQL's B-tree implementation in `nbtree` extends the basic
Lehman-Yao B-link-tree with several critical features not present
in goopg.

### Page Deletion (`_bt_pagedel`)

When a page becomes empty (all items moved to the right sibling
during splits, or deleted by VACUUM), PostgreSQL removes it from
the tree by:

1. **Lock** the page, its left sibling, and its parent.
2. **Unlink** the page by updating the left sibling's `op.next`
   pointer to skip the deleted page.
3. **Recycle** the page (add to the free space map for reuse).

This prevents the tree from growing monotonically. goopg does not
implement page deletion, so the tree grows without bound as
INSERTs and UPDATEs occur.

### Dedup (PostgreSQL 13+)

When multiple index entries have identical keys, PostgreSQL can
store them as a single "dedup" entry with multiple ItemPointers.
This reduces index size and fan-out. goopg always stores one
entry per tuple.

### Fast Root Optimisation

When the entire tree fits on a single page, PostgreSQL keeps it
as a "root-only" tree with no internal pages. The `BTreeMeta`'s
`fastroot` / `fastlevel` fields support this. goopg has the
metapage fields (`FastRoot`, `FastLevel`) but always traverses
through the normal root path.

### Incomplete Split Recovery

If a crash occurs during a page split, the B-tree might be in an
inconsistent state with the left page's high key still referencing
the right page but the parent not yet updated. PostgreSQL detects
this during search by following right-link chains (`op.next`).
goopg has the same right-link recovery logic in `descendToLeaf`.

### Sort Build (`_bt_spool` + `_bt_leafbuild`)

PostgreSQL builds indexes by:
1. **Sort** all tuples into key order using a dedicated sort
   operation (`_bt_spool`).
2. **Bulk-load** the sorted tuples into leaf pages, creating
   internal pages bottom-up (`_bt_leafbuild`).

This is O(N log N) with sequential I/O, far faster than the
row-by-row insertion that goopg uses, which is O(N log N) with
random I/O. For a 1M-row table, this is the difference between
seconds and minutes.

### GiST and SP-GiST

PostgreSQL also implements GiST (Generalised Search Tree) and
SP-GiST (Space-Partitioned GiST) for geometric, full-text, and
other non-default data types. goopg only supports B-tree.

## goopg Improvement Analysis

### P0: Bulk-Load for Index Creation

Replace the row-by-row `Insert` loop in index creation with a
sort-then-bulk-load approach:

1. Collect all (key, ItemPointer) pairs into a slice.
2. Sort by key using `sort.Slice`.
3. Build leaf pages sequentially (fill each page, chain via
   `op.next`).
4. Build internal pages bottom-up from the leaf level.

**Impact on pgbench -i:** The "primary keys" phase (31s at
scale=3) would drop to < 2s. This is the single largest
optimisation for data loading.

### P1: Page Deletion

Implement `deletePage(blk)` that:
1. Locks the page, its left sibling, and the parent (via `splitMu`).
2. Updates the left sibling's `op.next`.
3. Marks the page as free for reuse.

**Impact:** Prevents unbounded index growth under heavy
UPDATE/DELETE workloads.

### P2: Dedup

When inserting a duplicate key, instead of creating a new `item`,
append the ItemPointer to the existing item's list.

**Impact:** Reduces index size for non-unique indexes with many
duplicate keys.

## References

- goopg: `internal/access/btree/btree.go`
- PG B-tree overview: `postgres/src/backend/access/nbtree/README`
- PG insert: `postgres/src/backend/access/nbtree/nbtinsert.c`
- PG search: `postgres/src/backend/access/nbtree/nbtsearch.c`
- PG sort/build: `postgres/src/backend/access/nbtree/nbtsort.c`
- PG page deletion: `postgres/src/backend/access/nbtree/nbtdel.c`
- PG dedup: `postgres/src/backend/access/nbtree/nbtdedup.c`
