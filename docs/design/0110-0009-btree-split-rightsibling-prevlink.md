# 0110-0009 — B-tree split: relink the old right sibling's prev-link

Status: accepted (2026-06-14, loop #30)
Milestone item: M0110-0007
Related: `0002-0002-btree-concurrency.md` (Landing 3a atomic split record),
`0055-0003-btree-page-deletion-and-recycling-protocol.md` (the `RightSibNewPrev`
precedent), `0110-0005-verify-heapam-engine.md` (the real-producer harness that
found the bug).

## Problem

When a B-tree page `L` (block `blk`) splits, a new right page `R` (block
`rightBlk`) is spliced in between `L` and its former right sibling `S`
(`oldNext = L.btpo_next` before the split). The split path correctly set:

- `R.btpo_prev = blk`, `R.btpo_next = oldNext`
- `L.btpo_next = rightBlk`

but **never updated `S.btpo_prev`**, which kept pointing at `blk` instead of the
newly inserted `rightBlk`. The doubly-linked sibling chain was therefore left
half-relinked after every **non-rightmost** split.

Sorted/append-only inserts split only the *rightmost* page on a level (no `S`),
so the bug was invisible there — it only manifested on middle-of-level splits,
i.e. any non-append insert pattern (random PKs, UUIDs, secondary indexes). The
real-producer amcheck harness (`verify_nbtree_realtree_test.go`) with shuffled
inserts surfaced it: the sibling-link tier flagged
`left link/right link pair … not in agreement`.

`btpo_prev` is load-bearing. Page deletion
(`internal/access/btree/btree_vacuum.go`) reads `op.Prev` to find the left
sibling and WAL-logs `RightSibNewPrev` to relink it on page removal; a stale
left-link could mislead that relinking and any backward navigation. PostgreSQL
fixes this inside the atomic `_bt_split` WAL record, which locks the original
right sibling and stamps its left-link under the same record
(`nbtxlog.c` `xl_btree_split`; the SPLIT redo applies the `rnext` page).

## Fix

`insertIntoBlock` (`internal/access/btree/btree.go`), on a non-rightmost split
(pre-split `op.Next != InvalidBlockNumber`):

1. After redistributing items and stamping `L`'s high key / incomplete-split
   flag, pin+lock the old right sibling `S` (block `oldNext`) and set
   `S.btpo_prev = rightBlk`.
2. Fold `S`'s post-relink image into the **same atomic split WAL record** so
   crash recovery never observes a half-relinked chain. The record now carries
   an optional third page.
3. Stamp the record's LSN onto all three page headers; unlock `S` before
   walking up to the parent (same point the existing code drops `L`/`R`).

When no WAL writer is wired (test helpers), the fallback `MarkDirty` path dirties
`S` too (best-effort, in-memory tree stays correct), mirroring the existing
left/right fallback.

### Lock ordering

The relink acquires latches strictly left→right: `blk` → `rightBlk` → `oldNext`
(`oldNext` is logically to the right of `rightBlk`). This matches PostgreSQL
`_bt_split` (which locks `buf` → `rbuf` → `sbuf`) and the Lehman-Yao safe
direction, so it cannot deadlock against a concurrent split descending from the
left. `oldNext` can be neither `blk` (different live page) nor `rightBlk`
(freshly allocated / recycled-from-free-list, never a live sibling).

## WAL record format change

`internal/wal/recovery.go`, `RecordKindBtreeSplit` (kind byte 3):

- Header grows 18→22 bytes (`btreeSplitHeaderSize`): a `SibBlk uint32` field is
  appended after `RightBlk`. `SibBlk = InvalidBlockNumber` (0xFFFFFFFF) means a
  rightmost split with no third page.
- Payload is `left ‖ right` (2 pages, rightmost split) or `left ‖ right ‖ sib`
  (3 pages, non-rightmost split). Decode infers the count from payload length
  and cross-checks it against `SibBlk` (mismatch → error).
- `replayBtreeSplit` applies `left`, then `right` (Extend-or-Write — the new
  right may not yet exist on disk), then `sib` (always WriteBlock — `S` predates
  the split).

The on-disk **page** format is unchanged (the `btpo_prev` opaque field already
existed); only the WAL record format changed, so existing checkpointed data dirs
remain readable. WAL streams written by a prior binary that contain old 2-page
non-rightmost split records do not exist (the prior binary never emitted a third
page and always used the 2-page form), and a format change requires the usual
re-init for any uncheckpointed WAL.

### Signature ripple

`storage.LogBtreeSplitFunc`, `btree.LogSplitFunc`, `adaptPoolLogSplit`, and the
`initdb/open.go` `logBtreeSplit` closure all gained `sibBlk storage.BlockNumber,
sibPage storage.Page` trailing parameters.

## Tests / gates

- `TestVerifyBtreeEngineDetectsStaleSiblingLinkOnRealTree` (a detection
  assertion) was flipped to `TestVerifyBtreeEngineSilentOnRealShuffledInt4` — the
  shuffled tree's sibling-link tier is now silent, proving the relink lands on
  every middle split.
- `TestReplayBtreeSplitAtomicNonRightmost` — replays a 3-page record and asserts
  `S` is relinked on disk; the existing rightmost-split replay test still passes.
- `TestEncodeDecodeBtreeSplitRoundTrip` extended with the 3-page form.
- `TestSplitInvokesLogSplit` extended to assert the sibling-page invariants
  (sib page present iff `sibBlk != InvalidBlockNumber`).
- Gates: `go test -race ./internal/access/btree ./internal/wal ./internal/mvcc
  ./internal/storage` PASS; `go test ./internal/amcheck ./internal/executor
  ./internal/initdb` PASS; `go build ./...` clean; TPC-H spotcheck PASS
  (Q12=2/Q13=33).
