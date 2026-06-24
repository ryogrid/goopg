Loop #13 (this run): M0118-0008 — `partition-concurrent-attach` **PROMOTED**
(design 0118-0079). All 3 permutations byte-for-byte vs PG 18.3. COMMITTED + pushed.

## What landed (spec fully promoted, closes 0118-0075..0078 chain)
Two final gaps closed:
- **Gap 1 — INSERT routing-path lock (perms 1 & 3):** new
  `lockRoutingPathPartitions(ctx, named, leaf)` in operators_storage.go walks the
  parent chain from the routed leaf up to (excluding) the named INSERT target and
  takes a txn-scoped `RowExclusiveLock` (via `acquireWriteLockTxn`) on each
  INTERMEDIATE partition. Wired into `insertOp.Next` right after routing resolves
  the leaf. Locks `tpart_default` so a routed `INSERT INTO tpart` contends with a
  concurrent ATTACH's `AccessExclusiveLock` (0118-0076). Self-compatible/DML-grade
  ⇒ no blast radius; single-level partitioned + non-partitioned INSERTs = no-op.
- **Gap 2 — fresh snapshot for ATTACH re-scan (perm 3):**
  `checkDefaultPartitionDataConflict` (operators_ddl_partition.go) now refreshes
  `synthCtx.Snap = TxnMgr.SnapshotFor(ctx.Tx)` before the conflict scan (the
  attaching statement's snapshot predated the lock wait, so it couldn't see the
  concurrent INSERT's just-committed rows). Mirrors detach-4 0118-0064. → 23P01.

Files: internal/executor/operators_storage.go, internal/executor/operators_ddl_partition.go,
internal/testport/isolation_port_test.go (TestPort_IsolationPartitionConcurrentAttach
strict), docs/design/0118-0079 + README, port-status CSV + 3 regen md, ledger, fix_plan.

Gates: strict PASS (3 perms); 14 partition/DDL strict siblings PASS single run
(DetachPartitionConcurrently1/2/3/4 + AlterTable1/2/3 + CreateTrigger + InheritTemp
+ TruncateConflict + ClusterConflict{,Partition} + VacuumConflict); go test
./internal/executor/ PASS; -race partition/insert paths; go build ./... clean;
gen-oracle-port-status/gen-isolation-coverage/gen-oracle-inventory regen clean;
make ralph-state-guard (before status block); pgbench smoke = pre-commit hook.

## M0118-0008 hard tail (remaining, all Effort-L) — next loop picks one
- **alter-table-4**: INHERITS + per-session MVCC catalog cross-session visibility.
- **reindex-concurrently-toast**: real TOAST relations (reltoastrelid=0) as catalog
  objects + `allow_system_table_mods`.
- **WHERE CURRENT OF positioned UPDATE/DELETE**: project-wide; parsed (`CurrentOf`)
  but no executor site consumes it — needs per-row CTID capture in the cursor + a
  CTID-restricted rewrite.
Note: every partition spec in M0118-0008 (partition-concurrent-attach,
partition-drop-index-locking, detach-partition-concurrently-1/2/3/4) is now PROMOTED.
