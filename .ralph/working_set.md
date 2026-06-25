(idle — nothing in flight)

Last loop (#62) COMPLETE + committed: M0118-0005 `fk-partitioned-2`
**PROMOTED** → pass (design 0118-0121). This CLOSES the M0118-0005 FK /
referential-integrity isolation group (all 7 specs strict).

Spec: an FK referencing a PARTITIONED table (`pfk(a) references ppk`, both
list-partitioned). Two divergences fixed in `internal/executor/operators_fk.go`:

- **Gap A (RR/SSI INSERT side):** `scanTableForMatchFKWait` waits on the parent
  row's in-flight key-changing xmax, refreshes snapshot, re-scans — correct under
  READ COMMITTED (row gone → 23503) but under REPEATABLE READ / SERIALIZABLE PG
  raises `40001 could not serialize access due to concurrent update`
  (heap_lock_tuple HeapTupleUpdated). Added: after the wait + move-partition
  check, if `ctx.Tx.Isolation != ReadCommitted` && updater committed → 40001.
- **Gap B (partitioned-parent DELETE naming):** `DELETE FROM ppk` enters
  `enforceFKOnDelete` with the partitioned parent, firing parent-named
  `assertNoChildRows` (ppk / pfk_a_fkey). PG fires the LEAF clone (ppk1 /
  pfk_a_fkey_1). Fix: route deleted row to leaf via `routeToPartition`; skip the
  parent-named NO ACTION/RESTRICT assert when the row lives in a partition leaf
  (the unconditional `fkChildWaitForInFlightInsert` still gives `<waiting ...>`);
  run `enforceFKOnDeletePartitionAncestor` from the leaf.
- **Gap B follow-up (fk-snapshot regression):** routing exposed that
  `fkDeleteAncestorPass` raised immediately, breaking fk-snapshot's legal
  delete+re-insert under a DEFERRABLE INITIALLY DEFERRED FK. It now queues a
  deduped deferred check + skips the immediate raise inside an explicit txn.

Files: internal/executor/operators_fk.go,
internal/testport/isolation_port_test.go (TestPort_IsolationFkPartitioned2),
docs/test-port CSV+md, docs/design/0118-0121 + README.

NEXT remaining M0118 (all Effort-L unbuilt subsystems): index-only-bitmapscan
(real Bitmap Heap Scan + BitmapOr / EXPLAIN DECLARE CURSOR), predicate-gin/gist
(int[]/point + GIN/GiST AMs), predicate-hash (coarse SIREAD over-detects),
deadlock-parallel (lock-group), stats (pg_stat_* cumulative subsystem). Probe
each with a throwaway zz_probe test first to rank by first-divergence cost.
