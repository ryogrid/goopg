# Working Set (carried from loop 9, 2026-06-12)

## Completed this loop

**M0100-0006 — InsertConflictSpecconflict speculative insertion (perms 1–4)** — DONE
- Phase B first call: applyInsert now calls encodeArbiterKey BEFORE writeHeapRowReturning,
  inserts arbiter btree entry with pre-computed key.
- probeSpeculativeConflict: scans arbiter btree after applyInsert, skips own-xmin rows,
  detects concurrent commits during Phase B blocking window.
- cancelSpeculativeRow: stamps xmax=selfXID on speculatively-inserted heap row when conflict found.
- maintainUniqueIndexesForInsertSkipArbiter: like maintainUniqueIndexesForInsert but skips arbiter OID.
- DO UPDATE entry: explicit ExecBuildArbiterKey equivalent (2 NOTICEs before in-progress wait).
- applyUpdate: explicit encodeArbiterKey for updated row's btree entry (2 NOTICEs at completion).
- DO NOTHING: doubled to 2 encodeArbiterKey calls (4 NOTICEs total at completion).
- Perms 1–4 all pass; test diff now starts at L503 (perm 5 only).
- Perm 5 deferred to M0100-0006b (requires spectoken/transactionid pg_locks infrastructure).
- PASS count: 21 (no change — test still SKIP due to perm 5)

## Current PASS / SKIP

PASS (21): ReadWriteUnique, LockCommittedUpdate, LockCommittedKeyupdate,
  InsertConflictDoUpdate{,2,3,4}, InsertConflictDoNothing, FkSnapshot,
  PartitionKeyUpdate{1,2,3,4}, MergeDelete, MergeInsertUpdate, MergeMatchRecheck,
  MergeJoin, DropIndexConcurrently1, EvalPlanQualTrigger, EvalPlanQual, MergeUpdate

SKIP (1): InsertConflictSpecconflict (perm 5 requires spectoken infra)

## Next task (topmost unchecked in fix_plan.md)

**M0100-0006b — InsertConflictSpecconflict perm 5: spectoken infrastructure**
- Required: (a) implement speculative token acquire/release visible in pg_locks
  as locktype='spectoken', (b) expose own XID as transactionid ExclusiveLock in
  pg_locks, (c) implement `(step notices N)` wait annotation in isolation runner.

## Files of interest

- internal/executor/operators_upsert.go — speculative insertion changes (loop 9)
- internal/executor/operators_storage.go — maintainUniqueIndexesForInsertSkipArbiter (loop 9)

## Gates run

- go build: PASS
- executor/mvcc/server/planner -race: PASS
- Isolation suite: 21 PASS / 1 SKIP (diff starts at L503 perm 5 only)
- TPC-H spotcheck: SKIPPED (no data dir)
