# M-NIGHTLY ReadWriteUnique4 — a SERIALIZABLE Bitmap Heap Scan takes predicate locks

Status: landed 2026-10-10 (0cc3055db).

## Problem

Two isolation specs failed in every nightly from 2026-09-25 onward.

- **`TestPort_IsolationReadWriteUnique4`.** In permutation `r1 r2 w1 w2 c1
  c2`, both sessions read `MAX(invoice_number) … WHERE year = 2016` and then
  insert the same key. PG fails w2 with
  `could not serialize access due to read/write dependencies among
  transactions` (40001); goopg raised the duplicate-key error (23505).
- **`TestPort_IsolationTemporalRangeIntegrity`** failed at the same time.

## Cause

goopg's unique-conflict path already mirrors PG's `_bt_check_unique`.
After waiting out the conflicting inserter, a SERIALIZABLE writer runs the
SSI conflict-in walk (M0118-0001) before deciding between 40001 and
23505. That walk finds rw-conflicts only through the readers' SIREAD
locks.

The reads now plan as a **Bitmap Heap Scan** on the small table, since
bitmap paths became winnable. `bitmapHeapScanOp` called no SSI hook:

- no SIREAD lock;
- no conflict-out check on the tuples it read.

So r1's read was invisible to SSI and the dangerous structure never
closed. This is wrong results under SERIALIZABLE for any bitmap-planned
read (S2 class), not only this spec.

## PG behaviour

- **Index leaf pages.** `btgetbitmap` predicate-locks each leaf page it
  visits (`_bt_first` / `_bt_readpage`, `PredicateLockPage`).
- **Fetched tuples.** The bitmap heap fetch, `BitmapHeapScanNextBlock`
  (heapam_handler.c), calls `PredicateLockTID` and
  `HeapCheckForSerializableConflictOut` for every visible tuple, before the
  recheck and filter.

## Change

`internal/executor/operators_bitmap.go`, mirroring `indexScanOp`:

- **`openPrep`** takes a relation-grain SIREAD when the transaction is
  SERIALIZABLE. goopg keeps no btree-page SIREAD locks, and `indexScanOp`'s
  gap lock is relation-grain for the same reason. The lock is taken before
  the parallel early return, so serial and parallel scans both hold it.
  Temp tables and matviews are skipped, as in `indexScanOp`.
- **`fetchOneTuple`** now wraps `fetchOneTupleLocked`. It records what it
  saw of the TID under the page lock and makes the SSI calls after
  releasing it:
  - a visible tuple gets `ssiRecordTupleRead` (tuple SIREAD plus
    conflict-out against its xmin and xmax), whether or not the recheck or
    filter then rejects it;
  - a present-but-invisible tuple gets `ssiRecordInvisibleTupleRead` on its
    inserter (the phantom conflict-out);
  - a non-nil error is a 40001 mid-statement abort.

Every fetch path (serial, lossy page, parallel) goes through
`fetchOneTuple`.

## Verification

- **Unit test.** `TestSSI_BitmapHeapScanTakesPredicateLocks` plans the
  invoice read as a Bitmap Heap Scan and checks the SERIALIZABLE reader
  holds the relation SIREAD. It fails with HEAD's operator.
- **Isolation family:** all pass (rc=0). ReadWriteUnique4 and
  TemporalRangeIntegrity go FAIL → PASS. The non-strict suite's subtest
  flips are the parallel-setup catalog-mirror collisions already ledgered
  on 2026-09-19, at the same rate in every run.
- **TPC gates.**
  - TPC-H: spotcheck and acceptance arm pass on values.
  - TPC-DS: fire set has no fires at either scale; the SF0.25 sweep passes
    99/99 with 99 plan shapes unchanged.
  - These benchmarks run at READ COMMITTED, so the hooks are inert there.

## Not covered (ledgered)

- **Coarser locks than PG's.** The lock is relation-grain, not index-page
  grain, so a SERIALIZABLE bitmap reader conflicts with any insert into the
  table, where PG conflicts only with inserts into the leaf pages it read.
  That means more false-positive serialization failures than PG; `indexScanOp`
  already has the same gap.
- **Lossy pages.** These take tuple SIREADs per visible tuple. PG locks
  them too (each TID it returns), so this matches.
