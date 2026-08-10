Task: M0130-S11.5b-3 — the split record's block 3 (RM_BTREE content parity).
COMPLETE, committed, pushed.

Key finding: an internal btree page is NEVER inserted into for its own sake —
the only thing that lands on one is a separator pushed up by a split ONE LEVEL
DOWN, whose page is still flagged BTIncompleteSplit at that moment. Upstream
therefore clears that flag inside `_bt_split`'s own critical section and
registers the child as backup block 3 under `if (!isleaf)`. goopg had neither
half, and the missing block was fatal rather than lossy:
`XLogReadBufferForRedo` PANICs on an unregistered block id, so
`btree_xlog_split`'s unconditional `_bt_clear_incomplete_split(record, 3)` at
level > 0 takes a real standby down on the first goopg internal split.

Landed: `insertIntoBlock` carries upstream's `cbuf` as `childBlk`, threaded
from the three places a separator is pushed up (splitPage recursion,
finishSplit, createNewRoot's lost-the-race fallback); Invalid for a leaf tuple.
The split path pins the child while holding this level's latches (DESCENT
direction — cannot deadlock against a reader), clears the flag, logs block 3
with no data and stamps the record LSN. `clearIncompleteSplit` now no-ops when
the flag is already clear. `EncodeBtreeSplitPG` refuses BOTH violations of
`!isleaf` (level>0 without a child; a leaf carrying one). Replay clears block 3
FIRST, as upstream does. Hook signature `LogSplitFunc`/`LogBtreeSplitFunc` grew
childBlk (btree, bufpool, initdb/open.go, btree_test.go).

Guards: internal/wal/btree_split_pg_test.go (+3 tests) and
internal/access/btree/btree_test.go's TestInternalSplitLogsAndClearsChild —
4000 wide-key inserts force a real internal split, and NOT reaching one is a
failure, not a skip. Gates: btree/wal/storage/amcheck/initdb PASS; units PASS;
btree -race PASS; pgbench smoke PASS (commit hook). tpch-spotcheck / TPC-DS
SF0.5 NOT run — no on-disk shape change, no REINDEX debt.

Next step (re-read the fix_plan banner first):
1. **M0130-S11.5d — `XLOG_BTREE_UNLINK_PAGE`**, the last record in S11.5 and
   the one with a documented reason to stay native (an emit-time FPI can be
   stale against a concurrent split on another *BTree — the PG-faithful form
   needs incremental link patches). Then S11.5b-2 (blocked on the dedup pass
   fused into splitPage), then S11.6 (flip relhasindex).

In-flight: none.
