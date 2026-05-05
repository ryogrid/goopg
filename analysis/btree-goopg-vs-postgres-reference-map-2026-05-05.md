# B-Tree Reference Map: goopg vs PostgreSQL

Date: 2026-05-05

This companion file indexes concrete code anchors used by the main report.

## goopg anchors

## Concurrency and split protocol

- internal/access/btree/btree.go:434
  - Concurrency model comment (reader latch model, split serialization)
- internal/access/btree/btree.go:449
  - BTree struct
- internal/access/btree/btree.go:454
  - splitMu field
- internal/access/btree/btree.go:849
  - Insert
- internal/access/btree/btree.go:878
  - tryInsertNoSplit
- internal/access/btree/btree.go:919
  - insertIntoBlock
- internal/access/btree/btree.go:1019
  - split WAL hook branch (if bt.logSplit != nil)

## Page/item handling and split geometry

- internal/access/btree/btree.go:700
  - pageItems (expands posting items)
- internal/access/btree/btree.go:756
  - findChildBlockDirect (internal-page binary search)
- internal/access/btree/btree.go:981
  - count-based split midpoint (mid := len(allItems) / 2)
- internal/access/btree/btree.go:1151
  - insertItemSorted (decode-all + rewrite-all page update)

## Search/scan

- internal/access/btree/btree.go:1198
  - Search
- internal/access/btree/btree.go:1245
  - RangeScan

## HighKey constraints

- internal/access/btree/btree.go:45
  - MaxHighKeyLen constant (32)
- internal/access/btree/btree.go:1000
  - split failure when separator key exceeds MaxHighKeyLen
- internal/access/btree/posting_test.go:115
  - tests intentionally constrain key length to fit MaxHighKeyLen

## Dedup scope

- internal/access/btree/bulkload.go:439
  - deduplicateToRawItems (bulk path dedup)
- docs/design/0047-0003-deduplication.md
  - documents that incremental Insert decompacts touched pages

## Vacuum/page deletion

- internal/access/btree/btree_vacuum.go:31
  - VacuumIndexPages
- internal/access/btree/btree_vacuum.go:174
  - unlinkEmptyLeaf
- internal/access/btree/btree_vacuum.go:347
  - resetToEmptyRoot
- docs/design/0047-0002-page-deletion.md
  - limitations list (leaf-only deletion scope, WAL/autovacuum gaps)

## Build-time memory model

- internal/executor/operators_ddl.go:522
  - bulkBuildBTree comment: collects all entries in memory
- internal/executor/operators_ddl.go:540
  - collectBTreeEntries

## PostgreSQL anchors

## Search/insert concurrency protocol

- postgres/src/backend/access/nbtree/nbtsearch.c:215
  - _bt_moveright
- postgres/src/backend/access/nbtree/nbtsearch.c:458
  - _bt_binsrch_insert
- postgres/src/backend/access/nbtree/nbtinsert.c:39
  - _bt_findinsertloc
- postgres/src/backend/access/nbtree/nbtinsert.c:325
  - rightmost-leaf target block fastpath in _bt_search_insert

## Split lifecycle and WAL semantics

- postgres/src/backend/access/nbtree/README:620
  - WAL considerations for split/incomplete split protocol
- postgres/src/backend/access/nbtree/README:668
  - INCOMPLETE_SPLIT description

## Deletion protocol and recycling

- postgres/src/backend/access/nbtree/nbtpage.c:1773
  - _bt_pagedel
- postgres/src/backend/access/nbtree/nbtpage.c:2314
  - _bt_unlink_halfdead_page
- postgres/src/backend/access/nbtree/nbtpage.c:2986
  - pending deleted pages recycling into FSM

## Dedup in steady-state write path

- postgres/src/backend/access/nbtree/nbtdedup.c:58
  - _bt_dedup_pass

## Additional design notes

- docs/design/0002-0002-btree-concurrency.md:160
  - Landing 3b planned work and explicit note on skipped Prev update
- docs/design/0047-0001-btree-bulk-load.md
  - bulk build architecture
- docs/design/0047-0003-deduplication.md
  - goopg dedup design and stated limitations
