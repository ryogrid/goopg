# 0149\-0002 — recovery/t/036\_truncated\_dropped.pl: replay against shorter relation files

Status: landed 2026\-11\-23. TestPort\_Recovery036TruncatedDropped passes 8/8 assertions
all\-goopg and on a PG 18\.3 cold start of each crashed goopg directory \(M0149 C3\).

## The test

Five scenarios, each followed by stop\('immediate'\) and a restart:

1. VACUUM's prune records for a relation that existed at the checkpoint and was then
   dropped.
2. The same for a relation created after the checkpoint.
3. Prune records for blocks that TRUNCATE later removed, with FPIs \(a CHECKPOINT before
   VACUUM\). Then 10 new rows, and the assertion `count, sum = 10|55`.
4. The same without FPIs.
5. VACUUM's own tail truncation after deleting the upper half. Then rows at TIDs that
   existed before, and the assertion `510|136304|2|1010`.

## What failed, and the fixes

- **The harness ran each script as one transaction.** tapnode passed scripts with
  `psql -c`; Cluster.pm uses stdin. See 0149\-0001b.
- **A DROP wrote a goopg\-private record.** goopg's visibility map logged
  `RecordKindHeapVisible` with the drop flag through the custom rmgr 128. PG 18\.3
  stops recovery on it with `FATAL: resource manager with ID 128 not registered`, so a
  PG server could not recover any directory in which a heap was dropped.
  - PG writes no such record. The commit record lists the dropped relations, and
    xact\_redo\_commit's DropRelationFiles removes every fork. goopg's replay does the
    same \(dropXactRelFiles\).
  - DROP and TRUNCATE now forget the map through `VisibilityMap.DropRelationNoWAL`.
- **TRUNCATE was not WAL\-logged.**
  - goopg truncates in place, keeping the relfilenode, and wrote no truncate record.
    Replay therefore rebuilt the old pages from the earlier inserts. The new rows'
    INSERT\+INIT re\-initialised block 0 only, so 887 old rows came back with the 10 new
    ones \(897\).
  - TRUNCATE now logs RelationTruncate's `XLOG_SMGR_TRUNCATE` \(blkno 0,
    SMGR\_TRUNCATE\_ALL, XLR\_SPECIAL\_REL\_UPDATE, the transaction's xid\) for the heap and
    each index, before the file shrinks \(`logRelationTruncate`\). This is the record
    PG writes for its own in\-place case, a relation created in the same transaction.
  - Temporary and unlogged relations are not logged \(RelationNeedsWAL\).
  - A rolled\-back TRUNCATE restores its pages from the undo snapshot. Each restored
    page is now logged as a full\-page image, or replay would truncate and never bring
    the rows back. Verified: BEGIN; TRUNCATE; ROLLBACK, then a crash, keeps every row
    and the index on goopg and on PG.
- **VACUUM's tail truncation was not WAL\-logged.** `Pool.LogSmgrTruncateTo` existed
  but nothing installed it. open.go now logs `XLOG_SMGR_TRUNCATE` \(blkno = the kept
  blocks, SMGR\_TRUNCATE\_ALL\) and flushes it before the file shrinks, as
  RelationTruncate does.
- **Freeze plans were zeros.**
  - `EncodeHeapFreezePG` wrote one plan with xmax, t\_infomask2 and t\_infomask all 0.
    heap\_execute\_freeze\_tuple stores the plan's fields into each tuple, so PG's
    replay set natts to 0 and read every frozen column as NULL. Scenario 5 then
    returned `510|11055|1000|1010`.
  - The plans are now built from the frozen tuples' own headers and grouped as
    heap\_log\_freeze\_plan groups them, sorted by \(xmax, infomask2, infomask\) with
    offsets ascending inside each plan.
  - goopg still freezes by rewriting xmin to FrozenTransactionId, and plans carry no
    xmin. PG's replay therefore keeps the original \(committed\) xmin, while goopg's
    replay writes FrozenTransactionId. The two pages differ only in xmin, and both
    read the same rows.

## Not done

- TRUNCATE keeps the relfilenode. PG assigns a new one \(RelationSetNewRelfilenumber\),
  so the files and pg\_class.relfilenode differ from PG after a TRUNCATE.
- goopg's freeze does not set HEAP\_XMIN\_FROZEN as PG's heap\_prepare\_freeze\_tuple
  does.
- The executor's TRUNCATE truncate record is not flushed before the file shrinks
  \(RelationTruncate flushes when a VM or FSM exists\).
