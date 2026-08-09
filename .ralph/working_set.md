(idle — nothing in flight)

Last loop: M-NIGHTLY `TestPort_IsolationMergeUpdate` (AI-20260809-020705-018) —
the only reproducing failure of the 23 testport items in nightly run
`20260809-020705`. FIXED and committed.

Root cause was NOT in MERGE. The three cross-partition UPDATE stamp sites
treated `PageSetHeapTupleMovedPartition` as a *replacement* for the ordinary
delete-half stamp, so cmax was never written. Upstream `heap_delete` writes
cmax (`heapam.c:3065`) and only then adds the moved-partitions sentinel
(`:3071`) — the sentinel is an addition, not an alternative. With cmax
unwritten, the tuple's stale `t_cid` (its inserting cmin) stood in for cmax and
`mvcc.TupleVisible`'s `effXmax == currentXID` arm read `cmax >= curcid` as
"deleted by a later command — pre-image visible", so the moving transaction kept
seeing its own moved-away row. Fixed by unifying all three sites behind
`stampMovedPartitionOldTuple` (`internal/executor/operators_storage.go`).

Two facts worth carrying (they cost most of the loop's diagnosis time):
- The bug is invisible to every session except the writer, and only fires when
  the stale cmin is >= the writer's current command id. A same-transaction
  insert-then-move — the shape most unit tests and a hand-built two-psql repro
  use — always has cmin < curcid and looks correct. The isolation spec hit it
  only because its multi-statement `setup` block gave the rows a high cmin.
- `framework.IsolationRunner.RunAndCompare` already dumps goopg's full actual
  output to `/tmp/iso_actual_out.txt`. Read that before theorising; and a
  one-permutation spec + a throwaway test calling `RunSpec` directly reproduces
  in ~1s versus minutes of manual psql session juggling.

Design: addendum in
`docs/design/0100-0005n-cross-partition-update-moved-tuple-error.md` (+ README
index row updated). 1 ledger row: goopg never allocates combo CIDs
(`mvcc.AdjustCmax` has no non-test callers), so a same-transaction
insert-then-delete loses its cmin — pre-existing and wider than this fix.

Gates run: `TestPort_IsolationMergeUpdate` PASS; `TestStampMovedPartitionOldTupleWritesCmax`
(both sub-tests) PASS; `TestPort_IsolationPartitionKeyUpdate1..4` /
`MergeDelete` / `MergeInsertUpdate` PASS; units precommit PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=35); pgbench hook PASS at
commit; `make ralph-state-guard` OK (auto-repaired the stale completed marker).

NEXT LOOP (state, not authority — re-read the `## Current Priority` banner).
All M0130 slices are `[x]` and every AI- item from run `20260809-020705` is now
closed, so M-NIGHTLY has no open work pending the next nightly run. Below the
banner sit the carried M0119-0006 remainder (enum expr keys, checkunique posting
lists, box/int4range/int4[]/interval encodings, unscoped whole-DB pg_amcheck)
and the un-run TPC-DS SF0.5 sweep.

In-flight: none.
