# Module: `internal/access/nbtree`

The **B-tree index access method** — a faithful port of PostgreSQL's
`nbtree` (`src/backend/access/nbtree/`). It implements search, insert, page
split, deduplication, posting-list compression, and WAL-logged mutations, all
in a **PG-18 byte-identical on-disk format** (8 KiB pages, BTPageOpaque layout,
pivot / posting-list items, `BtMetaPageData`).

This is the index layer that `CREATE INDEX` builds, `SELECT`/`UPDATE`/`DELETE`
scans, and VACUUM purges dead entries from. All operator-facing code paths
(`indexScanOp`, `indexOnlyScanOp`, UPSERT arbiter, FK probes) route through
`BTree.Search` / `RangeScan`.

## Key Files

- `btree.go` (4,227) — `BTree` struct: `Open`/`OpenWithOptions`/`Create`/
  `Search`/`Insert`/`RangeScan`/`RangeScanWithPos`, page pinning (`pinR`/`pinW`),
  descent (`descendToLeaf`), split (`finishSplit`/`createNewRoot`/`refillDeduplicated`),
  dedup (`dedupConsolidate`), page-item iteration (`pageItems`/`PageItemKeys`).
- `pgpage.go` — B-tree page opaque data (`BTPageOpaque`), line-pointer helpers,
  `PageItemID`/`PGBTCycleId`.
- `pgtuple.go` — tuple encoding/decoding: `PGBTItemRaw`/`PGBTPivotRaw`,
  key-at-prefix, posting-list entry.
- `pgitemcodec.go` — item codec: `encodeItem`/`decodeItem`, `itemEncodedSize`,
  key-datum flattener, suffix truncation.
- `pgformat.go` — index format descriptors: `indexFormat` with `pageItems`/
  `parse`/`compare`/`encode`/`decode` by type family.
- `pgcompare.go` / `pgcompare_types.go` — key comparison functions: `CompareKeys`,
  per-type comparators (int4, int8, int128, numeric, varchar, char, timestamp,
  float8, oid, uuid, inet, enum, text, bpchar, bytea, date, time, timetz).
- `pgkeycmp.go` — key-at-prefix comparison for dedup and split.
- `pgsplit.go` / `pgsplitleft.go` — page split: `pgsplit`/`pgsplitleft`,
  split-point selection, posting-aware refill.
- `pgnewroot.go` — new-root creation (`pgnewroot`).
- `pgpagedel.go` / `pgtruncate.go` / `dead_purge.go` — page deletion,
  tree truncation, dead-item cleanup.
- `pgdelete.go` — LP_DEAD deletion pass (`pgdelete`).
- `posting.go` — posting-list helpers: `SwapPosting`, `PGBTPostingRaw`,
  `PostingLen`, `PostingDecode`, `PostingEncode`.
- `bulkload.go` — bulk-load construction (`bulkload` / `buildBulkLoadTree`).
- `lpdead_kill.go` — `killTID` / `killRange` for LP_DEAD entry reuse.
- `latch_release.go` — latch-based page-lock release for concurrent access.
- `btree_vacuum.go` — index vacuum: `btreeVacuumIndex`, `readInternalFirstChildBlock`.
- `replay.go` — WAL redo replay for btree opcodes (insert, split, dedup, delete,
  newroot, mark-page-halfdead, unlink-page, vacuum).
- `parse_err_dump.go` — page-dump helper for corrupt-page diagnostics.

## Public API

```go
func Open(pool *storage.Pool, rel storage.RelFileNode) (*BTree, error)
func OpenWithOptions(pool *storage.Pool, rel storage.RelFileNode, opts Options) (*BTree, error)
func Create(pool *storage.Pool, rel storage.RelFileNode) (*BTree, error)   // CREATE INDEX
func (bt *BTree) Search(key []byte) (storage.ItemPointer, bool, error)   // point lookup
func (bt *BTree) Insert(key []byte, ptr storage.ItemPointer) error
func (bt *BTree) RangeScan(lo, hi []byte, fn func(key, ptr) bool) error
func (bt *BTree) RangeScanWithPos(lo, hi []byte, loExclusive, hiExclusive bool, ...) error
```

## Internal structure

- **Page layout** — each page is 8 KiB with a `BTPageOpaque` at
  `SizeOfPageHeaderData` (24 bytes) carrying `btpo_flags` (leaf/root/meta/
  half-dead/deleted/has-garbage), `btpo_prev`/`btpo_next` (sibling links),
  `btpo_level`, `btpo_cycleid`. The metapage (block 0) carries
  `BtMetaPageData{magic, version, root, level, fastroot, fastlevel}`.
- **Search** — `Search` descends from the root by `findChildBlock` (binary
  search on the page) to a leaf, then `sort.Search` + `pageItems` to find the
  exact entry. Posting-list entries are expanded to individual TIDs.
- **Insert** — `Insert` descends to a leaf, `insertItemSorted` binary-searches
  the insertion point, and calls `tryInsertNoSplit` (append to existing page)
  or `finishSplit` (split the page, promote the pivot). `dedupConsolidate`
  runs opportunistic deduplication before splitting.
- **Split** — a page split produces a left page, right page, and a high-key
  pivot promoted to the parent. `pgsplit`/`pgsplitleft` select the split point
  (`compactSplitLoc`); `finishSplit` writes the new halves and recurses.
  Posting-list items are refilled by `refillDeduplicated`.
- **Posting lists** — a posting list packs multiple heap TIDs under one key
  (`PGBTPostingRaw`). `appendSorted` appends TIDs; `SwapPosting` exchanges TIDs
  during insert-into-posting. Dedup (`dedupConsolidate`) collapses same-key
  items into postings.
- **Key encoding** — `pgformat.go` dispatches per-type encoders/decoders
  (`EncodeInt4`, `EncodeVarchar`, `EncodeNumericKey`, `EncodeTimestamp`, …).
  The key format is PG-identical (4-byte prefix + datum bytes).
- **Comparators** — `CompareKeys` uses the `indexFormat.compare` function,
  which delegates to per-type comparators in `pgcompare.go` (family-ordered:
  int4, int8, int128, numeric, varchar, …).

## Dependencies

- **Used by** — `internal/executor` (index scans, DML, UPSERT, FK),
  `internal/initdb` (bootstrap index construction), `internal/access/amcheck`
  (verification), `internal/storage` (buffer-pool integration).
- **Uses** — `internal/storage` (buffer pool, pages, page I/O), `internal/access/transam`
  (visibility, xid), `internal/catalog` (type/opclass lookup for key encoding),
  `internal/utils/misc` (GUCs).

## Notable patterns / gotchas

- **PG-identical format** — the page/tuple/opaque format matches PG 18.3
  byte-for-byte (block 0 = metapage `BTreeMagic=0x0539`, `BTreeVersion=4`,
  `SizeOfBTPageOpaque=16`). A vanilla PG standby can read a goopg-authored
  btree index (and vice versa).
- **Posting-list expansion** — `pageItems` expands posting lists into individual
  `(key, TID)` pairs; `PageItemKeys` returns the raw keys (one per posting).
  Callers expecting per-TID entries must handle expansion; callers verifying
  key order must use `PageItemKeys`.
- **Dedup** — dedup is triggered before a split (`dedupConsolidate`); it is
  opportunistic and non-blocking. The dedup-on-write ordering: first try the
  page, then dedup, then split.
- **Key-at-prefix** — `pgkeycmp.go` supports suffix-truncated keys (the
  "key-at-prefix" optimization) for internal pages, where the high key is a
  prefix of the full key.
- **WAL redo** — `replay.go` handles all btree WAL opcodes (insert, split upper,
  split left, dedup, delete, newroot, mark-page-halfdead, unlink-page, vacuum,
  meta-cleanup, reuse-page). The redo path must be byte-identical to the
  primary's mutation — the S21b milestone closed the opcode coverage gap.
- **Bulk load** — `bulkload.go` constructs a sorted B-tree from sorted input,
  building pages bottom-up during `CREATE INDEX` and `B-tree bulk inserts`.
- **Fast-path** — `tryInsertOnCachedRightmost` caches the rightmost leaf for
  monotonic insert sequences (e.g., `INSERT INTO t VALUES (generate_series(1,N))`),
  skipping the root-to-leaf descent.