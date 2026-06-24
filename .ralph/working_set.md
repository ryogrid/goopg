Loop #14 (this run): M0118-0008 — `alter-table-4` **enabler 0118-0080** (NOT a
promotion). Perms 1 & 2 (NO INHERIT, NO INHERIT + INHERIT) now byte-for-byte vs
PG 18.3. First divergence advanced from the very first `<waiting ...>` marker to
perm 3. COMMITTED + pushed (pending).

## What landed
Deferred-DDL-until-commit for table inheritance (mirrors DROP INDEX 0118-0074 /
ATTACH 0118-0077). The SELECT side already locks each inheritance child AccessShare
(`collectInheritanceDescendants`→per-child SeqScan→`acquireScanReadLockTxn`); only
DDL-side pieces were missing:
- `AlterTableInherit`/`AlterTableNoInherit` take a txn-scoped AccessExclusiveLock on
  the child via `acquireDDLLockTxn` (no-op in autocommit/system catalogs).
- Inside an explicit txn (`(*ddlOp).inheritDeferSession()`) the register/unregister
  + INHERIT column-copy is deferred: `BasicSession.pendingInheritChng`
  (`PendingInheritanceChange` + Add/Take/CancelToDepth), applied at commit by
  `ApplyPendingInheritanceChanges` (executor execCommit + server dispatch),
  discarded by `DiscardPendingInheritanceChanges` (ProcessRollbackUndos) / to-depth
  in rollbackToSavepointOp. INHERIT validation (self/circular/dup) stays immediate.
- New `catalog.InMemory` O(1) counter `Has/Mark/UnmarkInheritanceChangePending`
  bypasses the cross-session plan cache while pending (`inheritanceChangePending`
  in both dispatch guards) so the 2nd `s2sel` re-plans → 1 (perm1) / 101 (perm2).

Files: internal/catalog/catalog.go (+inheritance_change_pending_test.go),
internal/executor/session.go, operators_ddl.go, operators_tx.go
(+pending_inheritance_change_test.go), internal/server/dispatch.go,
dispatch_extended.go, docs/design/0118-0080 + README, fix_plan, ledger.

## Next step (perms 3 & 4 — alter-table-4 full promotion)
Both need their own deferred-DDL + post-lock re-check on top of this foundation:
- **perm 3 (`DROP TABLE c1`):** defer the catalog removal to commit +
  AccessExclusiveLock on c1; after s2sel acquires the child lock, detect c1 was
  dropped and SKIP it (try_relation_open→NULL) so SUM excludes it (→1). goopg
  removes the table immediately and the inheritance scan errors on a vanished child.
- **perm 4 (`ALTER COLUMN a TYPE float`):** defer the column-type change + after
  s2sel locks c1, re-validate child vs parent → ERROR "attribute a of relation c1
  does not match parent's type".
Probe with a throwaway `RunAndCompare` test (status="defer", read .Diff). Spec stays
`defer` under TestPort_IsolationSuite until all 4 perms match.

## M0118-0008 hard tail remaining (all Effort-L)
- alter-table-4 perms 3 & 4 (above).
- reindex-concurrently-toast: needs real TOAST relations as catalog objects
  (reltoastrelid=0) + allow_system_table_mods; global-setup fails at
  `reltoastrelid::regclass::text` (no toast rel). Bigger subsystem.
- WHERE CURRENT OF positioned UPDATE/DELETE (project-wide; parsed CurrentOf, no
  executor site consumes it).

## Gates run (this loop)
build+vet clean; go test ./internal/{catalog,executor,server}/ PASS; -race
catalog + executor DDL/tx/savepoint/partition PASS; new units PASS; no regression
across InheritTemp/DetachPartitionConcurrently1..4/PartitionConcurrentAttach/
AlterTable1/AlterTable3/CreateTrigger/TruncateConflict; make ralph-state-guard OK;
pgbench smoke = pre-commit hook.
