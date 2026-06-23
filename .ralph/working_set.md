Loop #22: M0118-0008 — detach-partition-concurrently-4 PROMOTED (design 0118-0064, all 21 perms byte-for-byte). COMMITTED? pending commit at loop end.

What landed (two FK behaviours closed the last 3 WHERE-CURRENT-OF perms; NOT positioned-DML):
1. UPDATE now fires the RI_FKey_check parent-existence assertion (operators_fk.go +
   operators_storage.go). goopg only ran checkFKInsert from insertOp; updateOp did no
   parent lookup. New updateOp.childFKsToRecheck() (FKs whose referencing cols are in the
   SET list — mirrors PG firing the RI AFTER-trigger only on a key-column change, bounds
   blast radius to FK-key UPDATEs on FK tables) + recheckChildFKs() (delegates to new
   checkFKInsertForConstraints). Wired into ALL 3 update write paths (Next seqscan /
   updateViaIndex / updateWithFrom) right after BEFORE triggers, before the heap write.
2. DETACH re-validates RI_PartitionRemove_Check AFTER the hybrid wait (operators_ddl.go,
   ~line 5350, before Phase-2 finalize). First detachPartitionFKRefCheck runs before the
   wait under the stmt-start snapshot (misses a row a waited-on session commits during the
   wait); re-run with FRESH snapshot (TxnMgr.SnapshotFor(Tx).Clone) + PartitionDetachEpoch=0
   so routeToPartition keeps the now-pending child in the routing set ⇒ d4_fk_a_fkey_1.

Files: internal/executor/operators_fk.go, operators_storage.go, operators_ddl.go,
internal/testport/isolation_port_test.go (new TestPort_IsolationDetachPartitionConcurrently4),
docs/test-port/postgres-oracle-target-inventory.csv (failed→pass) + regen .md,
docs/design/0118-0064-*.md + README index, fix_plan + deferral_ledger.

Gates run: strict TestPort_IsolationDetachPartitionConcurrently4 PASS (21 perms);
detach-1/2/3 + Fk{Snapshot,Contention,Deadlock2}/ReferentialIntegrity/TemporalRangeIntegrity
+ PartitionKeyUpdate1..4 + Merge{Update,Delete,InsertUpdate,MatchRecheck,Join} +
InsertConflictDoUpdate{,2,3,4} PASS; regress-port foreign_key/update/constraints/inherit
no regression (exit 0); -race ./internal/executor/; go build clean. pgbench smoke = pre-commit hook.

Next step: commit + push. Then M0118-0008 tail: WHERE CURRENT OF positioned-DML
(project-wide, ledger), alter-table-4 (INHERITS + transactional-DDL cross-session
visibility), partition-concurrent-attach, partition-drop-index-locking (pg_locks),
reindex-concurrently-toast (toast relations + allow_system_table_mods).
