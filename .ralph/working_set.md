Task: M0130-S11.5b-1 — `XLOG_BTREE_SPLIT_R` (RM_BTREE content parity).
COMPLETE, committed, pushed.

Key finding: the split record's three blocks each have a DIFFERENT correct
answer, dictated by `btree_xlog_split` (nbtxlog.c:180-352), not by taste.
Block 1 (new right sibling) MUST be content: redo rebuilds it from scratch on
every replay (XLogInitBufferForRedo + _bt_pageinit + _bt_restore_page, return
code ignored), so an image-only block 1 gets restored and then overwritten with
an EMPTY item area — the mirror of S11.5a's missing-main-data trap. Block 0
(left half) MAY be an image: PG reaches its incremental left-half rebuild only
under BLK_NEEDS_REDO, so an image takes BLK_RESTORED and skips it plus all
three offsets (upstream's own XLogRegisterBufData comment says so). Block 2
carries nothing — redo derives the back-link from block 1's tag.

Landed: `EncodeBtreeSplitPG` → main data `xl_btree_split{level, firstrightoff,
newitemoff, postingoff}` (10 B), SPLIT_R opcode, block 0 apply-FPI, block 1
WILL_INIT + PGRestorePageData, block 2 bare. The right page's OPAQUE is not on
the wire (redo derives it from level + block tags), so
`btree.SplitRightPageOpaque` is the one definition: `splitPage` now stamps it
(dropping the stale-from-birth BTP_HAS_GARBAGE inheritance, as upstream does)
and the encoder REFUSES a mismatching right page. New
`internal/access/btree/pgsplit.go`: SplitRightPageOpaque,
CheckSplitRightPageOpaque (comparison must run on the IN-MEMORY flag set —
readOpaque is the translation), ReplaySplitRightPage, ReplaySplitSiblingPrev.
Replay `replayDecodedXLogBtreeSplit` in recovery.go, per-limb pd_lsn.
Obsolete `TestEncodeBtreeSplitPGFPIReplay` deleted; its `splitTestPage` helper
(used by two other files) kept in the same file.

Guards: internal/wal/btree_split_pg_test.go. Gates: btree/wal/storage/amcheck/
initdb PASS; units PASS; pgbench smoke PASS (commit hook). tpch-spotcheck /
TPC-DS SF0.5 NOT run — no on-disk shape change beyond the right page's garbage
hint, no REINDEX debt; run before the next REINDEX-required slice.

Next step (re-read the fix_plan banner first):
1. **M0130-S11.5c — `XLOG_BTREE_VACUUM`**: `xl_btree_vacuum{ndeleted,
   nupdated}` + the deleted/updated offset arrays. Smaller than S11.5b was.
   Then S11.5b-2 (incremental left half — blocked on goopg's split fusing a
   dedup pass), S11.5b-3 (block 3 on an internal split), S11.5d (unlink_page).

In-flight: none.
