Task: M0130-S11.5a — the real `XLOG_BTREE_NEWROOT` (RM_BTREE content parity).
COMPLETE, committed, pushed.

Key finding (drives the whole S11.5 series): goopg's btree records already had
PG ENVELOPES — `rmgr_map.go` maps every kind onto RM_BTREE_ID with the right
nbtxlog.h opcode. What was missing is CONTENT, and an FPI-only body is not
"less faithful", it is UNREPLAYABLE by PG: PG runs the rmgr redo function
whether or not a backup image is present, and `btree_xlog_newroot` opens with an
unconditional `XLogRecGetData` cast. Those records worked only because goopg's
own `RmgrBtree` default arm restored the images.

Landed: `EncodeBtreeNewRootPG(rel, rootBlk, rootPage, leftChildBlk, metaBlk,
metaPage)` — main data `xl_btree_newroot{rootblk, level}`, block 0 root
WILL_INIT + item area as `_bt_restore_page` data, block 1 left child, block 2
metapage WILL_INIT + 28-byte `xl_btree_metadata` (tail padding IS wire format).
level/items derived from rootPage, metadata from metaPage. Block 1 MANDATORY at
level>0 (XLogReadBufferForRedo PANICs on an unregistered id) — encoder refuses.
`internal/access/btree/pgnewroot.go`: PGRestorePageData / PGParseRestorePageData
(one file — untagged run, framed only by t_info size; disagreement mis-BUILDS
the page), ReplayRestoreMetaPage (_bt_restore_meta: rebuild, not RMW),
ReplayClearIncompleteSplit. All format-free (recovery has no catalog).
Replay: `replayDecodedXLogBtreeNewRoot` + helpers `replayInitedXLogBlock` /
`replayExistingXLogBlock` (per-limb pd_lsn idempotency).
Hook signature grew leftChildBlk (bufpool.go, open.go, btree.go:createNewRoot,
btree_vacuum.go:resetToEmptyRoot passes InvalidBlockNumber = leaf root).
Obsolete `TestEncodeBtreeNewRootPGFPIReplay` deleted (asserted the FPI form).

Guards: internal/wal/btree_newroot_pg_test.go, internal/access/btree/
pgnewroot_test.go. Mutation-checked: ascending item order, dropped block-1 limb.
Gates: btree/wal/storage/amcheck/initdb PASS; units PASS; pgbench smoke PASS
(commit hook). tpch-spotcheck / TPC-DS SF0.5 NOT run — no on-disk shape change,
no REINDEX debt; run before the next REINDEX-required slice.

Next step (re-read the fix_plan banner first):
1. **M0130-S11.5b — `XLOG_BTREE_SPLIT_L`/`_R`** (largest remaining):
   `xl_btree_split{level, firstrightoff, newitemoff, postingoff}`, new item +
   left page's new high key as block-0 data, right page's tuples as block-1
   data — S11.5a's PGRestorePageData/PGParseRestorePageData ARE that half.
   Then S11.5c (vacuum), S11.5d (unlink_page, deliberately native today).

In-flight: none.
