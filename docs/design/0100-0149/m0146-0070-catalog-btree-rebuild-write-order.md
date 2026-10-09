# M0146-0070 — catalog btree rebuild write order

Status: done 2026-10-07 (`968141b8d`).

## Problem

A leaf overflow in a multi-level catalog btree falls back to
`rebuildSysBtreeWithNewEntry` (`sys_catalog_btree_multilevel.go`), which
writes a fresh bulk-layout image over the index. It wrote the blocks in
ascending order:

1. the metapage;
2. the existing leaf and internal pages;
3. the new tail blocks, extending the file with `PinNew`.

An error before or during step 3 left the metapage and internal pages of
the new layout pointing at blocks that did not exist yet. The error could
be a victim-flush failure inside `PinNew` or the `PinNew returned blk`
guard. The next descent then failed `pin leaf blk N: short read`.

## PG

nbtree never overwrites a tree in bulk, but its page-split code writes a
page before anything that points to it:

- `_bt_split` writes the new right sibling before the parent downlink;
- `_bt_newroot` writes the new root before the metapage.

A reader therefore never follows a link to an unwritten page.

## Change

The rebuild writes in that order:

1. **Extend first.** Extend the file and write the new tail blocks. Until
   step 3 nothing points at them. If a later step fails they stay
   unreachable, like the trailing blocks a shrinking rebuild already
   leaves.
2. **Existing pages, children first.** Rewrite the existing non-meta blocks
   in ascending order. The bulk layout (`assembleSysBtreeLevels`) puts the
   leaves at blocks 1..L and each internal level above the one below it,
   so ascending order writes children before parents.
3. **Metapage last.**

Pages are still pinned one at a time. A first version pinned every block
before writing any, which would have made the in-memory rewrite atomic.
The unit suite showed it failing `no available buffer (all pinned)` on the
small pool `internal/initdb` uses (`TestRuntimeSaveAndReloadCatalog`).

The two pin calls go through package variables (`sysBtreeRebuildPin`,
`sysBtreeRebuildPinNew`) so that a test can make one of them fail.

## Verification

`TestSysBtreeRebuildFailureLeavesOldTree` seeds a multi-level 2691 tree
sized so that its rebuild must extend the file. Each case then fails one
pin:

- **A failed extension** leaves the old tree byte-identical and readable.
- **A failed existing-page or metapage pin** leaves a tree that reads
  without error: no referenced block lies past EOF.
- **An unfailed rebuild** completes with every tuple in order.

Run against the old loop with the same seams, the extension case fails
`pin internal blk 7: short read at block`, the production signature.

Gates:

- units PASS;
- tpch-spotcheck PASS;
- acceptance arm 24/24;
- sf025 96/96, plan shapes 99/99;
- ea-ratchet PASS.

## Not covered (ledgered)

- **Mixed trees.** A pin failure while the existing pages are rewritten
  (step 2) can still leave a tree mixing old and new pages. Reads succeed,
  but the content may be inconsistent.
- **Crash atomicity.** The rebuild is not atomic across a crash. Each page
  is WAL-logged as its own full-page image, so recovery can replay a prefix
  of the sequence.

PG avoids both by WAL-logging each split as one multi-page record. goopg
would need the same record, or a shadow tree with an atomic metapage
switch.
