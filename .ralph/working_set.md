(idle — nothing in flight)

Loop #37: PROMOTED `vacuum-conflict` (16 perms) + `cluster-conflict` (2 perms)
(M0118-0008 eleventh promotion, design 0118-0040, second/third of the `*-conflict`
family) — both byte-for-byte vs PG 18.3.

Ownership-based maintenance privilege (unlike truncate-conflict's grantable priv):
1. New `catalog.Table.Owner` (role name; empty = bootstrap superuser OID 10;
   pg_class.relowner output unchanged — field is check-only).
2. `ALTER TABLE … OWNER TO role` records the owner: parser captures into
   `AlterTableStmt.OwnerTo` (was discarded at ddl.go main ALTER path); executor
   `execAlterTable` sets `tbl.Owner` + takes txn-scoped AccessExclusiveLock
   (no-op in autocommit — how the spec issues the grant). `current_user` sentinel
   → empty owner (bootstrap superuser).
3. `maintenancePermitted(ctx,tbl)` (operators_vacuum.go): superuser (NonSuperuserRole=="")
   always OK; else owner match (EqualFold) or HasTablePrivilege MAINTAIN. Wired into
   the EXPLICIT-target loop of expandVacuumTargets + expandAnalyzeTargets BEFORE any
   lock (mirrors expand_vacuum_rel no-lock pg_class ACL check) ⇒ unprivileged target
   skipped with `WARNING: permission denied to vacuum/analyze "X", skipping it`, no wait.
4. `clusterOp.Next` (operators_cluster.go) now takes a blocking AccessExclusiveLock
   via acquireRelLockMaybeTransient so CLUSTER waits behind SHARE UPDATE EXCLUSIVE.

Gates green: TestPort_IsolationVacuumConflict + ClusterConflict strict; all sibling
M0118-0008 specs (truncate-conflict/vacuum-skip-locked/vacuum-concurrent-drop/
create-trigger/sequence-ddl/reindex-*/inherit-temp); -race executor+catalog;
parser units; pgbench smoke 0-failed ~15.2k TPS. Design 0118-0040 + README + CSV/MD
port-status (D-002 rationale) + fix_plan + ledger updated.

Next step: M0118-0008 tail — `cluster-conflict-partition` (per-child lock enumeration)
is the closest sibling to this loop's work. Then `alter-table-{1,2,4}` (ADD/VALIDATE
CONSTRAINT lock semantics), partition ATTACH/DETACH specs, reindex-concurrently-toast,
vacuum-no-cleanup-lock, plpgsql-toast.
