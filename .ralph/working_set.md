Loop #21: M0118-0008 — detach-partition-concurrently-4 cursor + abort/cancel permutations LANDED (design 0118-0063, PARTIAL — NOT promoted).

What landed (three fixes; closes every detach-4 permutation EXCEPT the 3 WHERE CURRENT OF perms):
1. Eager cursor materialisation at DECLARE (internal/server/dispatch.go,
   DeclareCursorStmt branch): materializeCursor now runs at DECLARE instead of
   lazily on first FETCH (mirrors PG opening the portal + taking snapshot/locks
   at DECLARE). The materialising scan takes a txn-scoped AccessShare
   (acquireScanReadLockTxn, held to commit ⇒ concurrent DETACH … CONCURRENTLY
   parks behind the open cursor via waitForRelationLockers) AND buffers the
   declaration-time snapshot's rows (FETCH returns the pre-detach partition set).
   goopg already buffered all rows at first FETCH so only the timing shifts;
   executeFetch guards on !cur.Materialized.
2. Abort releases the RR/SSI pinned snapshot (internal/mvcc/manager.go +
   internal/server/conn_tx.go + dispatch.go): new ReleasePinnedSnapshot(handle)
   clears pinnedSnap WITHOUT ending the txn + broadcasts commitCond; new
   WaitForPinnedSnapshotsReleased waits until each slot is inTxn==0 OR
   pinnedSnap==false (now used by WaitForPinnedSnapshotsToCommit);
   connTxState.ReleasePinnedSnapshotOnFail(mgr) calls it after Fail() gated
   SavepointDepth()==0, wired at both Fail() sites. Mirrors PG AbortTransaction
   dropping the snapshot the instant a top-level stmt errors ⇒ a detacher
   waiting on an RR session unblocks at the error, BEFORE the explicit
   ROLLBACK/COMMIT (perm s1brr s1s s2detach s1insert s1c).
3. Cancel-message mapping (internal/executor/operators_ddl.go): the detach's
   WaitForPinnedSnapshotsToCommit result is mapped through lockWaitCancelError
   so a cancel reports 57014 "canceling statement due to user request" not the
   bare "context canceled".

Gates run: probe (RunAndCompare) first divergence moved spec L80 → the 3 updcur
perms (71-73). detach-1/2/3 + vacuum-no-cleanup-lock(cursor) + alter-table-1/2/3
+ truncate/vacuum/cluster-conflict + Fk(Contention,Snapshot)/ReferentialIntegrity
+ SimpleWriteSkew/ReadOnlyAnomaly + InheritTemp + DeleteAbortSavept{,2}/
AbortedKeyrevoke/SubxidOverflow strict PASS (no regression); -race ./internal/mvcc/...;
mvcc/server/executor units PASS; go build clean; state-guard OK (repaired).

Next step: detach-4 promotion needs ONLY positioned UPDATE/DELETE — `update
d4_fk set a=1 where current of f` (s1updcur, perms 71-73). CurrentOf is parsed
(parser.UpdateStmt.CurrentOf / dml.go) but NO server/executor site consumes it.
Impl: (a) capture each buffered cursor row's CTID at materialisation (the
MaterializedSlot carries ctidBlock/ctidOff/hasCTID but materializeCursor only
clones Row=[]Datum, dropping it); (b) track the cursor's current-position CTID;
(c) restrict the UPDATE/DELETE to that CTID (CTID-predicated rewrite or TID-scan).
Probe with internal/testport zz_probe_test.go (import testutil/cluster; RunAndCompare
→ log .Diff + /tmp/iso_actual_out.txt). Other M0118-0008 tail: alter-table-4
(INHERITS), partition-concurrent-attach, partition-drop-index-locking (pg_locks),
reindex-concurrently-toast (toast relations + allow_system_table_mods).
