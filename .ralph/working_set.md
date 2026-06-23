Loop #20: M0118-0008 — detach-partition-concurrently-4 FK behaviour landed (design 0118-0062, PARTIAL)

Landed the FK-current-epoch fix (NOT a promotion — detach-4 still defers on cursors).

What landed:
- internal/executor/operators_fk.go: snapDetachEpoch now returns the GLOBAL
  mvcc.CurrentPartitionDetachEpoch() instead of ctx.Snap.PartitionDetachEpoch.
  Root cause: detach-4 requires the FK existence check to reject inserting a
  value that lives only in a concurrently-detaching partition EVEN UNDER
  REPEATABLE READ (PG's RI_FKey_check runs under the latest snapshot). goopg
  filtered by the enforcing statement's snapshot epoch — correct for RC (fresh
  per-stmt snapshot) but wrong for RR (txn snapshot predates the detach ⇒ not
  filtered ⇒ FK wrongly found the value). Scoped to the two FK existence scans
  (scanTableForMatch/scanTableForMatchFKWait via allDescendants); ordinary
  query/cursor partition expansion still uses the snapshot epoch, preserving the
  RR-visible-row asymmetry. Dropped now-unused ctx param + os import.

Gates run: probe (RunAndCompare) — FK permutations RC+RR now byte-match, residual
diff confined to the 8 cursor permutations; detach-1/2/3 strict PASS;
FkSnapshot/FkContention/PartitionKeyUpdate1..4 PASS; executor+catalog units PASS;
go build ./... + go vet ./internal/executor/ clean; state-guard OK (repaired).

Next step: detach-4 cursor permutations need cursor-pinned-snapshot machinery
(two coupled pieces, land together):
  (1) Cursor snapshot pinning at DECLARE — goopg materialises a cursor lazily on
      first FETCH (cursorEntry, internal/server/conn_tx.go), so a cursor declared
      before a detach but fetched after sees the post-detach partition set (1 row)
      instead of its declaration-time set (2 rows). Capture the snapshot (incl.
      PartitionDetachEpoch) at DECLARE; expand partitions against that frozen
      epoch at FETCH.
  (2) Detacher waits on an open cursor — DECLARE CURSOR over the partitioned
      parent must register a pinned snapshot / relation lock so the concurrent
      DETACH … CONCURRENTLY blocks (<waiting>) and is cancellable. Today the
      cursor holds neither ⇒ detacher completes immediately ⇒ s1cancel no-op.
Probe with internal/testport zz_probe_test.go (RunAndCompare → log .Diff +
/tmp/iso_actual_out.txt). Other M0118-0008 tail: alter-table-4,
partition-concurrent-attach, partition-drop-index-locking, reindex-concurrently-toast.
