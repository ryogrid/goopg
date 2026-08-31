# 0110-0010 — B-tree vacuum: relink the *live* siblings when deleting adjacent empty leaves

Status: accepted
Milestone: M0110-0010
Related: 0110-0005 (verify-heapam/nbtree engine), 0110-0009 (split right-sibling prev-link)

## Problem

`VacuumIndexPages` (`internal/access/btree/btree_vacuum.go`) deletes every leaf
that becomes empty after dead-tuple pruning. It runs in two phases:

1. **PHASE 1** (the left-to-right leaf scan): for each leaf that loses all its
   items it sets `BTDeleted | BTHalfDead` on the page and records an
   `emptyLeafInfo{blk, firstKey, prev, next}` — capturing the leaf's `btpo_prev`
   / `btpo_next` *as they were during the scan, before any unlink*.
2. **PHASE 2** (`unlinkEmptyLeaf` / `unlinkEmptyLeafFPI`): for each recorded
   leaf, relink its left sibling's `Next` and its right sibling's `Prev` to
   bypass the leaf, remove the leaf's parent downlink, and mark the leaf fully
   deleted.

The bug: PHASE 2 relinks neighbours using the **captured** `prev`/`next`. When a
captured neighbour is *itself* one of the leaves being deleted in the same pass,
the pointer is stale. For an adjacent run `L0 → L1 → L2 → L3 → L4` with
`L1, L2, L3` all emptied:

- `unlink(L1)` sets `L0.next = L2`, `L2.prev = L0`
- `unlink(L2)` (captured prev=L1, next=L3) sets `L1.next = L3`, `L3.prev = L1`
- `unlink(L3)` (captured prev=L2, next=L4) sets `L2.next = L4`, `L4.prev = L2`

The surviving edges `L0.next` and `L4.prev` end up pointing at the **deleted**
block `L2`. The on-disk leaf sibling chain is structurally broken. amcheck's
sibling-link tier (`VerifyBtreeLevelSiblingLinks`) correctly flags
`downlink or sibling link points to deleted block`.

`btpo_prev`/`btpo_next` are load-bearing (backward scans, future page-deletion
relinking), and an adjacent dead-tuple run is the *common* case for a range
`DELETE` + `VACUUM`, so this is a genuine on-disk correctness gap.

Discovered 2026-06-15 by the real-producer page-deletion validation
(`internal/amcheck/verify_nbtree_pagedel_test.go`), detection pinned by
`TestVerifyBtreeEngineDetectsStaleSiblingLinkAfterAdjacentLeafDeletion`.

## Upstream reference

PostgreSQL's `_bt_unlink_halfdead_page` (`postgres/src/backend/access/nbtree/nbtpage.c`)
re-reads the **current** left/right siblings at unlink time and bails out
(deferring to a later pass) when a sibling is itself half-dead, rather than
trusting pointers cached earlier. The on-disk sibling chain is therefore never
left referencing a half-dead/deleted block.

## Fix

At unlink time, walk past any page that is itself `BTDeleted`/`BTHalfDead`
(i.e. being unlinked in the same pass) to find the **nearest live** left and
right sibling, then relink those:

```
leftLive  = liveSibling(leaf.prev, walk via btpo_prev)   // skip deleted/half-dead
rightLive = liveSibling(leaf.next, walk via btpo_next)
leftLive.next  = rightLive   (only if leftLive  is live)
rightLive.prev = leftLive    (only if rightLive is live)
```

Two facts make a simple skip-walk correct and **order-independent**:

- PHASE 1 stamps `BTDeleted | BTHalfDead` on **every** target leaf *before* any
  unlink runs, so all members of an adjacent run are already recognisable as
  "dead" from the very first `unlink` in PHASE 2.
- `recycleBlock` only appends the block to a free list; it does **not** wipe the
  page, so a deleted leaf retains its original `btpo_prev`/`btpo_next` and the
  chain through dead pages stays navigable until reuse.

So starting the walk from the (possibly now-dead) captured neighbour and
skipping dead pages always lands on the live page just outside the run. The
result is identical no matter which order the run's leaves are processed — which
also makes `CompleteDeferredDeletions` (post-crash completion, which iterates by
block number, *not* chain order) correct.

For the worked example the first `unlink(L1)` already computes
`leftLive = L0`, `rightLive = L4` and sets `L0.next = L4`, `L4.prev = L0`; the
subsequent `unlink(L2)` / `unlink(L3)` recompute the same edges and are
idempotent. Final state: `L0 → L4`, chain intact.

### No WAL format change

The `BtreeUnlinkPage` WAL record already carries arbitrary
`LeftSibBlk`/`LeftSibNewNext`/`RightSibBlk`/`RightSibNewPrev` fields, and
`replayBtreeUnlinkPage` applies whatever blocks the record names. The fix only
changes the *values* computed before the record is emitted, so the record
format, encode/decode, and replay are untouched. Both the WAL-emitting path
(`unlinkEmptyLeaf`) and the FPI fallback (`unlinkEmptyLeafFPI`) share the same
`liveSibling` computation so the sibling paths stay in agreement.

## Tests / gates

- `TestVerifyBtreeEngineDetectsStaleSiblingLinkAfterAdjacentLeafDeletion`
  flipped to a **silence** assertion
  (`TestVerifyBtreeEngineSilentAfterAdjacentLeafDeletion`).
- New `TestVacuumIndexPagesAdjacentLeafRunRelinksLiveSiblings` in
  `internal/access/btree/btree_vacuum_test.go` asserts, at the storage layer,
  that after an adjacent-run deletion every surviving leaf's `btpo_prev`/
  `btpo_next` references a live (non-deleted) block and the leaf chain from
  leftmost is intact.
- `go test -race ./internal/access/btree ./internal/wal ./internal/mvcc
  ./internal/storage ./internal/amcheck`.
- TPC-H spotcheck (`scripts/tpch-spotcheck.sh`).
```
