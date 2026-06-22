Loop #56: M0118-0008 — `DETACH PARTITION … CONCURRENTLY` wait enabler LANDED
(NOT a promotion; design 0118-0057).

Done: goopg's executor completed `ALTER TABLE … DETACH PARTITION … CONCURRENTLY`
synchronously (ignored the DetachConcurrently flag from 0118-0048), so the
isolation runner never rendered the `<waiting ...>` marker the four
detach-partition-concurrently-{1,2,3,4} specs need. Fix: in the
AlterTableDetachPartition case, when act.DetachConcurrently, after the
UnregisterPartitionChild, block on the existing (*Context).waitForRelationLockers
(WaitForLockers analog, 0118-0029) for BOTH the parent and detached-child
RelFileNodes. A partitioned-parent SELECT locks the children
(acquireScanReadLockTxn), so a concurrent reader holds the detached child's
AccessShare until commit and the detacher parks behind it. No lock of its own
(CONCURRENTLY contract); inherits 57014 cancellation (the detach-3/4
s1cancel(s2detach) path). Gated on DetachConcurrently → plain DETACH/FINALIZE
unchanged, blast radius nil.

Probe (throwaway): detach-1's `<waiting>`/`<... completed>` markers now
byte-match PG; first divergence advanced to perm 1's repeated-SELECT row content.
detach-{2,3,4} still defer cleanly (no hang/panic).

Files: internal/executor/operators_ddl.go (AlterTableDetachPartition case,
~L5199), docs/design/0118-0057 + README index, deferral_ledger.

Gates: probe confirms markers byte-match; go build ./... + go vet
./internal/executor/ clean; internal/executor tests PASS; pgbench smoke =
pre-commit hook.

Next step (detach-1 full promotion, in order — each its own loop/slice):
1. Cross-session plan-cache invalidation for partition DDL: a repeated
   `SELECT * FROM parent` in the same session reuses the cached Append over the
   ORIGINAL partition set, so even READ COMMITTED doesn't see the detached
   partition disappear (perm 1 still shows 2 rows). Same class as inherit-temp
   0118-0037's plan-cache bypass (dispatch.go sessionTempInheritanceActive gate
   at L730 + dispatch_extended sibling). Add a partition-membership-aware
   bypass/invalidation. This is the NEXT bounded slice — likely the cheapest
   real advance on detach-1's READ COMMITTED perms.
2. REPEATABLE READ snapshot-stable partition visibility (s1brr perms) =
   milestone-sized MVCC catalog-visibility subsystem, shared with alter-table-4
   + partition-concurrent-attach (probed: alter-table-4 needs s1's uncommitted
   NO INHERIT to be invisible to s2 until commit; goopg's shared catalog makes
   it immediately visible so s2 never even builds the child scan to block on).
3. detach-3/4 two-phase pending-detach state (inhdetachpending /
   pg_partition_tree / DETACH FINALIZE / cancel-then-resume).
partition-drop-index-locking = real pg_locks/pg_stat_activity 3-way-join view +
multi-level partition lock propagation (probe: s3getlocks returns 0 rows).
reindex-concurrently-toast = real auto-created TOAST relations +
allow_system_table_mods GUC.
