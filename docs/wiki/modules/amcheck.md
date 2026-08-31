# Module: `internal/access/amcheck`

The **index and heap verification** module — a Go port of PostgreSQL's
`contrib/amcheck`. It verifies B-tree index structure (item ordering, sibling
links, downlink consistency) and heap-table integrity (all-indexed checks,
tuple-header sanity, update-chain resolution). Not a real-time monitor; it is
run on demand (e.g., `pg_amcheck` TAP, `verify_heapam()` SRF, DBA diagnostics).

## Key Files

- `verify_nbtree.go` — `VerifyBtreePage`, `VerifyBtreeItemOrder`,
  `VerifyBtreeLevelSiblingLinks`, `VerifyBtreeParentDownlinks`: B-tree
  structural verification (item order, sibling link chain, parent→child
  downlink consistency at every level).
- `verify_nbtree_unique.go` — unique-constraint verification for B-tree indexes.
- `verify_heapam.go` — `VerifyHeapPage`, `VerifyHeapPageWithRel`: heap-page
  tuple-header sanity (xmin/xmax bounds, infomask consistency, HOT-chain
  resolution, update-chain non-circularity).
- `verify_heapam_relation.go` — relation-level heap verification (iterate
  all pages).
- `heapallindexed.go` — `HeapAllIndexedCheck`: cross-check that every heap TID
  has a matching index entry (bloom-filter-based, O(heap tuples) scan).
- `heapallindexed_heapscan.go` — heap scan side of the all-indexed check.
- `heapallindexed_relation.go` — relation-level all-indexed orchestration.
- `bloomfilter.go` — a space-efficient bloom filter for the all-indexed check
  (like PG's `bloom_filter.h`): `bloomCreate`, `bloomAddElement`,
  `bloomLacksElement`.

## Public API

```go
// B-tree verification
func VerifyBtreePage(bt *nbtree.BTree, blk, ...) error
func VerifyBtreeItemOrder(bt, blk, ...) error
func VerifyBtreeLevelSiblingLinks(bt, ...) error
func VerifyBtreeParentDownlinks(bt, ...) error

// Heap verification
func VerifyHeapPage(p storage.Page, blk, ...) Report
func VerifyHeapPageWithRel(rel, p, blk, ...) Report

// All-indexed check
func HeapAllIndexedCheck(rel, indexes, ...) error
```

## Internal structure

- **B-tree verification** — reads every page of the index, checks item order
  within each page, sibling `btpo_prev`/`btpo_next` links at each level, and
  parent→child downlink correctness. Customizable via `KeyComparator` for
  type-specific ordering checks.
- **Heap verification** — per-page tuple-header checks: xmin/xmax bounds,
  infomask consistency, `HEAP_HOT_UPDATED`/`HEAP_ONLY_TUPLE` flag correctness,
  HOT-chain traversal, update-chain non-circularity. `resolveXminStatus` uses
  the clog (or an injected `XidStatusFunc`).
- **All-indexed** — materializes all heap TIDs into a bloom filter, then
  streams every index entry; any TID missing from the bloom is a heap TID
  without an index entry (a candidate for index corruption).

## Dependencies

- **Used by** — `internal/testport` (TAP tests), `internal/executor`
  (verify-heapam SRF).
- **Uses** — `internal/access/nbtree` (B-tree page access), `internal/storage`
  (page/tuple decode), `internal/access/transam` (xid visibility, clog).

## Notable patterns / gotchas

- **Bloom filter sizing** — the bloom filter for the all-indexed check is sized
  `nBits * maxHashFuncs` where `nBits` is `2 * nHeapTuples`; a false positive
  rate of ~2⁻¹⁰ is acceptable.
- **Verification is read-only** — verification never modifies pages; it acquires
  page content locks (`pinR` → `RLock`) during the check.
- **HOT-chains** — `checkUpdateChains` walks the HOT chain on each page,
  verifying that each redirect points to a valid successor and the chain
  terminates at a live tuple without cycles.