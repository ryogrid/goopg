Loop #9 (this run): M0118-0008 — `partition-concurrent-attach` **enabler 0118-0075**
(NOT a promotion). Landed the default-partition conflict check on the ALTER
ATTACH path. COMMITTED. Spec stays `defer`.

## What landed
`ALTER TABLE … ATTACH PARTITION` now rejects attaching a non-default partition
over rows already in the parent's visible DEFAULT (PG `23P01`, mirrors
`ATExecAttachPartition` → `check_default_partition_contents`). The check existed
only on the CREATE TABLE PARTITION OF path; wired it into the
`AlterTableAttachPartition` executor case + made the message name the LEAF
default (nested sub-partitioned default).

- operators_ddl.go: ATTACH case calls `checkDefaultPartitionDataConflict`
  (gated `!poc.Default && !poc.IsHash`, `return err`).
- operators_ddl_partition.go: `checkDefaultPartitionDataConflict` walks down to
  the leaf default for the error name (detection unchanged — scans subtree via
  immediate default).
- attach_default_conflict_test.go (new): reject / no-conflict / nested-leaf.
- design 0118-0075 + README index; ledger + fix_plan note.

Key symbols: checkDefaultPartitionDataConflict, AlterTableAttachPartition case.

## Probe result (partition-concurrent-attach, BEFORE this loop)
First divergence L7: PG `s2i … <waiting ...>` then ERROR "violates partition
constraint"; goopg s2i succeeds immediately (no wait, no error), final 6 rows in
tpart_2 vs PG's 3. Perm 3: PG s1a `<waiting>` then ERROR "updated partition
constraint for default partition tpart_default_default would be violated".

## Why still deferred (the coupled milestone)
Full promotion needs ALL of (shared with alter-table-4):
(a) deferred-until-commit ATTACH visibility — a concurrent session must NOT see
    the uncommitted new partition, so its INSERT routes to the DEFAULT;
(b) ATTACH takes AccessExclusiveLock on the DEFAULT partition so the routed
    INSERT renders `<waiting ...>` until the attach txn commits;
(c) constraint re-validation after the wait sees the other session's committed
    rows (perm 3 error fires only then).
These three are interlocking — none alone moves a clean divergence boundary.
This is the per-session MVCC catalog visibility milestone.

## M0118-0008 hard tail (remaining, all Effort-L)
- alter-table-4 + partition-concurrent-attach: per-session MVCC catalog
  visibility (THE next milestone — highest leverage, unlocks two specs).
- reindex-concurrently-toast: real TOAST relations (reltoastrelid=0).
- WHERE CURRENT OF positioned UPDATE/DELETE: parsed (CurrentOf), no executor site.

Next step: start the per-session MVCC catalog visibility milestone; for
partition-concurrent-attach specifically, begin with (a) deferred-until-commit
ATTACH (mirror the loop-#8 DROP INDEX deferral: PendingPartitionAttach recorded
in the session, applied before TxnMgr.Commit on both commit paths) + the global
attach-epoch so older snapshots don't see the new child.

Gates run: go build ./... clean; new attach_default_conflict_test.go (3) PASS;
go test ./internal/executor/ PASS; TestPort_IsolationDetachPartitionConcurrently1
strict PASS; make ralph-state-guard (run before status block); pgbench smoke =
pre-commit hook.
