Task: M0130-S11.5d-1 — `XLOG_BTREE_MARK_PAGE_HALFDEAD` content parity.
COMPLETE, committed, pushed.

Key finding: page deletion is TWO records upstream and goopg's single native
`RecordKindBtreeUnlinkPage` covers the union. The phase-1 record goopg already
tagged RM_BTREE/0xB0 carried 16 bytes of {leafblk, flagsAfter} and NO registered
blocks — `btree_xlog_mark_page_halfdead` calls `XLogInitBufferForRedo(record, 0)`
unconditionally, so an unregistered block id PANICs the standby (S11.5b-3's
shape). It also has ZERO live emit sites: the `LogBtreeMarkPageHalfDead` hook is
wired initdb→storage.Pool and never called.

Two things upstream's redo DEFINES that goopg's page model lacks:
(a) a half-dead page IS its contents — one dummy SizeOfIndexTupleData high key
whose t_tid block half is the subtree top parent, which
`_bt_unlink_halfdead_page` reads to find the next page down;
(b) the parent mutation is a RETARGET (point poffset at the right neighbour's
child, delete the neighbour), absorbing the deleted key range RIGHTWARD —
goopg's `ReplayRemoveParentDownlink` deletes poffset and absorbs it LEFTWARD.
Same input, different parent page.

Landed: `EncodeBtreeMarkPageHalfDeadPG` (20-byte main data incl. the 2 C
alignment bytes after poffset; block 0 leaf WILL_INIT no data, block 1 parent;
leftblk/rightblk read off the logged page; refuses no-parent / poffset 0 /
topparent==leaf). `btree.ReplayMarkHalfDeadLeaf` + `btree.ReplayHalfDeadParent`
(PHYSICAL OffsetNumber, both P_FIRSTDATAKEY shapes; note resetPageItems
re-installs the high key itself — collect from PGFirstDataKey, not slot 1).
`replayDecodedXLogBtreeMarkPageHalfDead` + dispatch arm (block 1 then block 0,
upstream's order, per-limb pd_lsn).

Guards: internal/wal/btree_halfdead_pg_test.go (+3),
internal/access/btree/replay_halfdead_test.go (+3). Gates: btree/wal/storage/
amcheck PASS; units PASS; btree+wal -race PASS; pgbench smoke PASS (commit
hook). tpch-spotcheck / TPC-DS SF0.5 NOT run — replay-only, no emit site, no
on-disk shape change.

Next step (re-read the fix_plan banner first):
1. **M0130-S11.5d-2 — `XLOG_BTREE_UNLINK_PAGE`** (phase 2 encode+replay), then
   S11.5d-3 (rewire the emit sites: upstream's pin-left/target/right protocol +
   the retarget-and-delete parent mutation — see the ledger row), then S11.5b-2,
   then S11.6 (flip relhasindex).

In-flight: none.
