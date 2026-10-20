# M-NIGHTLY AI-20261020-013101-001 — b-tree descents step right from an unlinked page, and deletion re-tests emptiness under its latch

Status: landed 2026-10-20.
Evidence: `analysis/m-nightly/ai-20261020-013101-001/`.

## 1. Failure

The 2026-10-20 nightly units lane panicked in
`TestStructuralLockVacuumWithSplits` (eight inserters splitting pages while
vacuum deletes the pages between them):

```
panic: storage: unsupported line pointer state: slot=2 flags=0
  insertItemSorted ← insertItemSortedVerified ← tryInsertNoSplit ← Insert
```

It reproduces at GOMAXPROCS=2 about once per 40–100 runs. Two defects were
found, both of which PG's page-deletion protocol excludes.

## 2. Defect 1 — a descent lands on an unlinked page

goopg's descent does not latch hand over hand. `descend` pins a page, reads
it, releases it, then pins the child. A writer or reader can therefore hold a
block number whose page vacuum unlinks before the pin. `unlinkEmptyLeaf`
rewrites the target as a BTDeletedPageData tombstone
(ReplayUnlinkTargetPage):

- BTP_DELETED | BTP_HAS_FULLXID;
- pd_lower covering the 8-byte safexid.

`PGDataItemCount` reads those bytes as two line pointers, and the second one
is the `slot=2 flags=0` of the panic. A tombstone that happened to parse would
be worse: the insert would land in a page no downlink reaches.

**PG:** `_bt_moveright` (nbtsearch.c) treats `P_IGNORE` pages (deleted or
half-dead) like a page whose high key the search key exceeds: it follows
`btpo_next`. `_bt_readnextpage` skips them in scans. Deletion hands the
target's key range to its right sibling, so moving right is correct.

**Change:** `BTPageOpaque.isUnlinked` is BTDeleted without BTHalfDead.
Every site that pins a page it chose before latching it steps right from one:

- `descend`, at every level, since internal pages are unlinked by the
  cascade;
- `tryInsertNoSplit`, which defers to the slow path;
- the `insertIntoBlock` move-right loop;
- `Search`;
- `rangeScanPosLeaf`;
- `ScanCursor.Next`.

A deleted page with no right sibling is `_bt_moveright`'s "fell off the end
of index" (errFellOffEnd).

goopg's **phase-1** mark (BTDeleted|BTHalfDead, set by VacuumIndexPages) is
deliberately *not* skipped. Unlike PG's half-dead page, it leaves the parent
untouched: the page still owns its key range, and unlinkEmptyLeaf reverts the
mark when an insert repopulates it.

## 3. Defect 2 — deletion destroys a tuple inserted after its first check

`unlinkEmptyLeaf` re-checked emptiness under the leaf's latch, released it,
and only later latched left, target, right and parent to write the two
deletion records. A writer that inserted into the phase-1 page in that gap
lost its tuple to the tombstone rewrite.

**PG:** `_bt_pagedel` refuses a target that is not empty
(`P_FIRSTDATAKEY(opaque) <= PageGetMaxOffsetNumber(page)`) while holding its
exclusive lock.

**Change:** right after `acquireUnlinkPins` latches the target, a non-empty
target abandons the deletion (`abandonHalfDeadLeaf` clears the mark and keeps
the items). The latch is held through both records, so no insert can follow
the test.

## 4. Recycling, and the test

With both defects fixed, the stress test still lost about one key per ~100
runs. The key sat stranded in a live leaf outside its routed range. The test's
bare pool had no recycle horizon, and a horizon-less pool recycles a deleted
block at once (stamping safexid 0). A split reused the block while an inserter
that had descended to it still held its number.

PG never recycles before the deletion's safexid precedes every running xmin
(`BTPageIsRecyclable`), and production wires that horizon (initdb/open.go).
The test now installs a horizon that holds recycling for the round, modelling
its inserters as running transactions.

Gated runs: 0 failures in 600 with both fixes. Without the move-right, the
panic returned 3/3.

## 5. Pins

- `TestStructuralLockVacuumWithSplits`: the stress test, now
  recycle-gated. On a missing key it dumps every leaf that still holds the
  key. Without `isUnlinked` it panics again.
- `TestUnlinkRechecksEmptinessUnderTheTargetLatch`: deterministic. A
  test-only hook, `beforeUnlinkPins`, inserts into the emptied leaf inside the
  window; the key must survive the vacuum. Without the latched re-check,
  Search misses it.

## 6. Not done (ledgered)

- A pool without a recycle horizon recycles at once. PG has no such mode;
  only bare test pools hit it.
- `unlinkEmptyLeafFPI`, the FPI fallback when either deletion emitter is
  unwired, still has no latched emptiness re-check. It relinks siblings before
  marking the target, so the WAL path's abandon cannot be reused as is.
- goopg's phase-1 mark is not PG's half-dead step: PG retargets the parent
  while marking, goopg only when it unlinks.
