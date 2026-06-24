Loop #6 (this run): M0118-0008 — SELECT locks the scanned relation's indexes
(design 0118-0072). COMMITTED + pushed at loop end. NOT a promotion (closes
"blocker 2" of 4 for partition-drop-index-locking).

## What landed (enabler)
New helper (*Context).acquireScanIndexReadLocksTxn(tbl) in context.go: enumerates
Catalog.IndexesOnTable(tbl) and AccessShare-locks each via the existing
acquireScanReadLockTxn hook (held-to-commit in explicit txn / transient in
autocommit / system catalogs skipped). Wired into all 3 scan-open paths that
already take the table AccessShare lock:
- seq scan: operators_storage.go (o.tbl)
- index scan: operators_index.go openPrep (o.plan.Table)
- index-only scan: operators_indexonly.go (o.plan.Table)
PG locks ALL indexes of a scanned relation (get_relation_info→index_open), not
just the probed one — so a bare SELECT on a leaf partition now appears in pg_locks
holding AccessShare on its indexes, blocking a concurrent DROP INDEX (0118-0071).

Files: internal/executor/context.go (helper), operators_storage.go,
operators_index.go, operators_indexonly.go (wiring),
partition_drop_index_lock_test.go (new TestSelectLocksLeafPartitionIndexes),
docs/design/0118-0072-*.md + README index, deferral_ledger.md.

Key symbols: acquireScanIndexReadLocksTxn, acquireScanReadLockTxn,
catalog.IndexesOnTable, catalog.IndexRelFileNode.

## Probe result (2026-06-24)
1st s3getlocks now shows the open SELECT holding AccessShareLock|t on the leaf
table + BOTH leaf indexes (_subpart_child_id_idx{,1}) — 3 rows previously absent.
DROP-side rows unchanged. Spec still `defer`. Remaining first-getlocks gap is now
only the empty query column (blocker 3); 2nd getlocks shows 5 vs 6 rows (blocker 4).

## partition-drop-index-locking remaining blockers (resume point)
3. **pg_stat_activity idle-query retention**: s.query empty for idle-in-txn
   backends; goopg clears Query on return to idle (activity/registry.go
   UpdateState `else if state=="idle"`) and drops trailing ';'. PG retains the
   most-recent query for idle-in-txn. NEXT (mechanical-ish).
4. **Transactional-DDL cross-session catalog visibility** (MILESTONE-SIZED, shared
   with alter-table-4 / partition-concurrent-attach): 2nd s3getlocks must still
   show the dropped index's pg_class row + locks until s2commit; goopg removes
   from the shared in-memory catalog synchronously (5 vs 6 rows).

Next step: blocker (3) idle-query retention — retain the last query text (with
trailing ';') for idle-in-transaction backends in activity/registry.go so the
s3getlocks `query` column matches. Then the txnl-DDL visibility milestone (4).

## M0118-0008 hard tail (all Effort-L, deferred)
- partition-drop-index-locking: blockers 3/4 above.
- alter-table-4 + partition-concurrent-attach: transactional-DDL cross-session
  catalog visibility (milestone-sized MVCC catalog subsystem).
- reindex-concurrently-toast: real TOAST relations (reltoastrelid=0; text inline).
- WHERE CURRENT OF positioned UPDATE/DELETE: project-wide, parsed (CurrentOf) no executor site.

Gates run: go build ./... clean; TestSelectLocksLeafPartitionIndexes +
TestDropIndexLocksIndexRelationTree + TestCreateIndexRecursesPartitionTree PASS;
full ./internal/executor/ PASS; -race ./internal/lockmgr/; isolation no-regression
batch (Reindex*/MultipleCic/DropIndexConcurrently1/InheritTemp/CreateTrigger/
Truncate-Vacuum-Cluster-Conflict/AlterTable1-2-3) PASS; pgbench smoke=pre-commit.
