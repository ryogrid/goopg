Loop #15 (this run): M0118-0008 — `alter-table-4` **enabler 0118-0081** (NOT a
promotion). Perm 3 (`DROP TABLE c1`) now byte-for-byte vs PG 18.3 on top of
perms 1&2 (0118-0080). First divergence advanced from perm 3's `<waiting ...>`
(L43) to perm 4's ERROR line (L65). COMMITTED + pushed (pending at write time).

## What landed (perm 3)
Deferred-DROP-TABLE-until-commit + inheritance-child skip-if-vanished, mirroring
the DROP INDEX deferral (0118-0074):
- `dropTableByRef(name, tbl, allowDefer)` now takes a txn-scoped
  AccessExclusiveLock via `acquireDDLLockTxn` (no-op in autocommit). For a simple
  leaf (no partition/inheritance children, PartitionParentOID==0,
  DetachPendingEpoch==0, no temp shadow) under `allowDefer` (true only for the
  top-level non-CASCADE drop), it records `PendingTableDrop` and defers; else
  immediate via the renamed `dropTableByRefImmediate`. `ApplyPendingTableDrops`
  replays at commit on BOTH paths (execCommit + dispatch TxCommit). Cancel on
  ROLLBACK / to-depth on `rollbackToSavepointOp`.
- `planner.SeqScan.SkipIfVanished` set on inheritance children; `seqScanOp.Open`
  after the scan lock (where the wait ends) returns 0 rows if the child OID is
  gone from the catalog (else NBlocks O_CREATE-recreates the dropped relfile).

Files: internal/executor/session.go, operators_ddl.go, operators_tx.go,
pending_table_drop_test.go (NEW); internal/server/dispatch.go;
internal/planner/plan.go + planner.go; internal/executor/operators_storage.go;
docs/design/0118-0081 + README; deferral_ledger.

## Next step (perm 4 — ALTER COLUMN a TYPE float, last perm → full promotion)
`permutation s1b s1delc1 s1modc1a s2sel s1c s2sel` expects, after commit:
`ERROR: attribute "a" of relation "c1" does not match parent's type`.
The wait already works (s1delc1 holds c1's AccessExclusiveLock, deferred from
perm 1). Needed: after s2sel acquires the post-commit lock on c1, the
inheritance child scan must re-validate the child's column TYPES vs the parent
(PG `make_inh_translation_list` / `expand_single_inheritance_child`) and raise
the mismatch error. Implement as a post-lock type-match check in the
`SkipIfVanished` branch of `seqScanOp.Open` — but it needs the PARENT's expected
column types threaded into the child SeqScan (planner has both tbl=parent +
child). Verify ALTER COLUMN a TYPE float is visible post-commit (immediate or
needs deferral). Probe with a throwaway RunAndCompare (read .Diff). Spec stays
`defer` under TestPort_IsolationSuite until perm 4 matches → then promote to
strict TestPort_IsolationAlterTable4 + port-status CSV → port + regen md.

## M0118-0008 hard tail remaining (all Effort-L)
- alter-table-4 perm 4 (above) → then promote the spec.
- reindex-concurrently-toast: needs real TOAST relations as catalog objects
  (reltoastrelid=0) + allow_system_table_mods; global-setup fails at
  `reltoastrelid::regclass::text`. Bigger subsystem.
- WHERE CURRENT OF positioned UPDATE/DELETE (project-wide; parsed CurrentOf, no
  executor site consumes it).

## Gates run (this loop)
build+gofmt clean (only pre-existing go1.25/1.26 alignment noise in untouched
lines); go test ./internal/{executor,catalog,planner,server}/ PASS; -race
DDL/tx/savepoint/commit/rollback PASS; new TestPendingTableDropSession PASS; no
regression across AlterTable1/AlterTable3/InheritTemp/PartitionDropIndexLocking/
DetachPartitionConcurrently1/PartitionConcurrentAttach/TruncateConflict/
CreateTrigger; probe perm 3 byte-match; make ralph-state-guard (run pre-commit);
pgbench smoke = pre-commit hook.
